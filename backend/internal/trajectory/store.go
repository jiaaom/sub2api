package trajectory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	dirRaw     = "raw"
	dirSpool   = "spool"
	fileMeta   = "meta.json"
	fileIndex  = "index.ndjson"
	fileFormat = "FORMAT.md"
	dirAttempt = "attempts"
)

// diskStore 负责磁盘布局：spool/<id>/ → raw/<date>/u<uid>/<id>/。
type diskStore struct {
	root    string
	indexMu sync.Mutex
}

func newDiskStore(root string) (*diskStore, error) {
	s := &diskStore{root: root}
	for _, dir := range []string{root, filepath.Join(root, dirRaw), filepath.Join(root, dirSpool)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("trajectory: create %s: %w", dir, err)
		}
	}
	if err := s.writeFormatDoc(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *diskStore) spoolDir(id string) string { return filepath.Join(s.root, dirSpool, id) }

// rawRel 返回相对根目录的 raw 路径：raw/<UTC 日期>/u<uid>/<id>。
func rawRel(startedAt time.Time, userID int64, id string) string {
	return filepath.Join(dirRaw, startedAt.UTC().Format("2006-01-02"), "u"+strconv.FormatInt(userID, 10), id)
}

func (s *diskStore) writeFormatDoc() error {
	path := filepath.Join(s.root, fileFormat)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, []byte(formatDoc)) {
		return nil
	}
	if err := writeFileAtomic(path, []byte(formatDoc)); err != nil {
		return fmt.Errorf("trajectory: write %s: %w", fileFormat, err)
	}
	return nil
}

// probe 降级后探测存储是否恢复可写。
func (s *diskStore) probe() error {
	path := filepath.Join(s.root, dirSpool, ".probe")
	if err := os.WriteFile(path, []byte("ok"), 0o644); err != nil {
		return err
	}
	return os.Remove(path)
}

// commit 把 spool 中的 exchange 原子地移到 raw，并追加当日索引。
func (s *diskStore) commit(spool string, meta *Meta) (string, error) {
	rel := rawRel(meta.StartedAt, meta.UserID, meta.ExchangeID)
	if err := writeJSONAtomic(filepath.Join(spool, fileMeta), meta); err != nil {
		return "", err
	}
	dst := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(spool, dst); err != nil {
		return "", err
	}
	if err := s.appendIndex(meta.StartedAt, meta.indexEntry(filepath.ToSlash(rel))); err != nil {
		return rel, err
	}
	return rel, nil
}

func (s *diskStore) appendIndex(startedAt time.Time, entry IndexEntry) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	path := filepath.Join(s.root, dirRaw, startedAt.UTC().Format("2006-01-02"), fileIndex)
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	return errors.Join(werr, cerr)
}

// recoverSpool 启动时把上次进程残留的 exchange 标为 incomplete 并归档。
func (s *diskStore) recoverSpool(now func() time.Time) (int, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, dirSpool))
	if err != nil {
		return 0, err
	}
	recovered := 0
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := s.spoolDir(entry.Name())
		meta := loadSpoolMeta(dir, entry.Name())
		meta.Recovered = true
		if meta.Status == StatusComplete || meta.Status == StatusIncomplete || meta.Status == StatusError {
			// 最终 meta 已写出、只差 rename（例如关停时异步落定被打断）：数据完整，保留原状态。
			meta.Notes = append(meta.Notes, "commit resumed after restart")
			if _, err := s.commit(dir, meta); err != nil {
				errs = append(errs, fmt.Errorf("recover %s: %w", entry.Name(), err))
				continue
			}
			recovered++
			continue
		}
		meta.Status = StatusIncomplete
		finished := now()
		meta.FinishedAt = &finished
		reconcileAttemptsFromDisk(dir, meta)
		for i := range meta.Attempts {
			if meta.Attempts[i].Outcome == OutcomePending || meta.Attempts[i].Outcome == "" {
				meta.Attempts[i].Outcome = OutcomeAborted
			}
		}
		meta.Notes = append(meta.Notes, "recovered from spool after unclean shutdown")
		if _, err := s.commit(dir, meta); err != nil {
			errs = append(errs, fmt.Errorf("recover %s: %w", entry.Name(), err))
			continue
		}
		recovered++
	}
	return recovered, errors.Join(errs...)
}

func loadSpoolMeta(dir, id string) *Meta {
	meta := &Meta{}
	if raw, err := os.ReadFile(filepath.Join(dir, fileMeta)); err == nil {
		_ = json.Unmarshal(raw, meta)
	}
	meta.FormatVersion = FormatVersion
	if meta.ExchangeID == "" {
		meta.ExchangeID = id
	}
	if meta.StartedAt.IsZero() {
		if u, err := uuid.Parse(id); err == nil && u.Version() == 7 {
			sec, nsec := u.Time().UnixTime()
			meta.StartedAt = time.Unix(sec, nsec).UTC()
		} else if info, err := os.Stat(dir); err == nil {
			meta.StartedAt = info.ModTime().UTC()
		}
	}
	return meta
}

// reconcileAttemptsFromDisk 补齐 meta 中缺失、但磁盘上存在的 attempt 目录与文件名。
func reconcileAttemptsFromDisk(dir string, meta *Meta) {
	entries, err := os.ReadDir(filepath.Join(dir, dirAttempt))
	if err != nil {
		return
	}
	var indexes []int
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() {
			indexes = append(indexes, n)
		}
	}
	sort.Ints(indexes)
	byIndex := map[int]int{}
	for i, a := range meta.Attempts {
		byIndex[a.Index] = i
	}
	for _, n := range indexes {
		if _, ok := byIndex[n]; ok {
			continue
		}
		a := AttemptMeta{Index: n, Outcome: OutcomeAborted}
		attemptDir := filepath.Join(dir, dirAttempt, strconv.Itoa(n))
		_ = filepath.WalkDir(attemptDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			rel = filepath.ToSlash(rel)
			switch {
			case strings.Contains(d.Name(), "upstream.req"):
				a.RequestFile = rel
			case strings.Contains(d.Name(), "upstream.resp"):
				a.ResponseFile = rel
			}
			return nil
		})
		meta.Attempts = append(meta.Attempts, a)
		byIndex[n] = len(meta.Attempts) - 1
	}
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// bodyFile 带缓冲、带上限的追加写文件。写失败后静默停止（错误通过 err 上报一次）。
type bodyFile struct {
	f         *os.File
	w         *bufio.Writer
	n         int64 // 实际流过的字节数（含被截断部分）
	written   int64
	limit     int64
	truncated bool
	err       error
	closed    bool
}

func createBodyFile(path string, limit int64) (*bodyFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &bodyFile{f: f, w: bufio.NewWriterSize(f, 64<<10), limit: limit}, nil
}

func (b *bodyFile) write(p []byte) {
	if b == nil || b.closed {
		return
	}
	b.n += int64(len(p))
	if b.err != nil {
		return
	}
	if room := b.limit - b.written; int64(len(p)) > room {
		b.truncated = true
		if room <= 0 {
			return
		}
		p = p[:room]
	}
	m, err := b.w.Write(p)
	b.written += int64(m)
	if err != nil {
		b.err = err
	}
}

func (b *bodyFile) close() error {
	if b == nil || b.closed {
		return nil
	}
	b.closed = true
	ferr := b.w.Flush()
	cerr := b.f.Close()
	if b.err == nil {
		b.err = errors.Join(ferr, cerr)
	}
	return b.err
}

// writeWholeFile 一次性写入已在内存中的 body（A、B）。返回是否截断。
func writeWholeFile(path string, data []byte, limit int64) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	truncated := false
	if int64(len(data)) > limit {
		data = data[:limit]
		truncated = true
	}
	return truncated, os.WriteFile(path, data, 0o644)
}

package trajectory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewWritesFormatDocAndLayout(t *testing.T) {
	root := t.TempDir()
	rec, err := New(Options{Root: root}, nil)
	require.NoError(t, err)
	defer rec.Close()
	for _, p := range []string{dirRaw, dirSpool} {
		info, err := os.Stat(filepath.Join(root, p))
		require.NoError(t, err)
		require.True(t, info.IsDir())
	}
	doc, err := os.ReadFile(filepath.Join(root, fileFormat))
	require.NoError(t, err)
	require.Contains(t, string(doc), "format v1")
}

func TestRecoverSpoolArchivesUnfinishedExchanges(t *testing.T) {
	root := t.TempDir()
	spool := filepath.Join(root, dirSpool)

	// 1) 有 meta 的半成品：attempt 0 只写了部分响应。
	idA := uuid.Must(uuid.NewV7()).String()
	dirA := filepath.Join(spool, idA)
	require.NoError(t, os.MkdirAll(filepath.Join(dirA, "attempts", "0"), 0o755))
	started := time.Date(2026, 10, 9, 23, 59, 0, 0, time.UTC)
	metaA := Meta{FormatVersion: 1, ExchangeID: idA, Status: StatusInProgress, StartedAt: started, UserID: 3,
		Attempts: []AttemptMeta{{Index: 0, Outcome: OutcomePending}}}
	require.NoError(t, writeJSONAtomic(filepath.Join(dirA, fileMeta), metaA))
	require.NoError(t, os.WriteFile(filepath.Join(dirA, "attempts", "0", "upstream.resp.sse"), []byte("data: half"), 0o644))

	// 2) 连 meta 都没写出的目录：从 UUIDv7 推断开始时间，从磁盘补 attempt。
	idB := uuid.Must(uuid.NewV7()).String()
	dirB := filepath.Join(spool, idB)
	require.NoError(t, os.MkdirAll(filepath.Join(dirB, "attempts", "0"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dirB, "attempts", "0", "upstream.req.json"), []byte("{}"), 0o644))

	rec, err := New(Options{Root: root}, nil)
	require.NoError(t, err)
	defer rec.Close()
	require.Equal(t, int64(2), rec.Stats().ExchangesRecovered)

	entries, _ := os.ReadDir(spool)
	require.Empty(t, entries, "spool must be empty after recovery")

	gotA := readMeta(t, filepath.Join(root, "raw", "2026-10-09", "u3", idA))
	require.Equal(t, StatusIncomplete, gotA.Status)
	require.True(t, gotA.Recovered)
	require.Equal(t, OutcomeAborted, gotA.Attempts[0].Outcome)

	dirs := rawExchanges(t, root)
	require.Len(t, dirs, 2)
	var gotB Meta
	for _, d := range dirs {
		if strings.HasSuffix(d, idB) {
			gotB = readMeta(t, d)
			require.Contains(t, d, "/u0/")
		}
	}
	require.Equal(t, idB, gotB.ExchangeID)
	require.False(t, gotB.StartedAt.IsZero())
	require.Len(t, gotB.Attempts, 1)
	require.Equal(t, "attempts/0/upstream.req.json", gotB.Attempts[0].RequestFile)

	idx := readFile(t, root, "raw/2026-10-09/index.ndjson")
	require.Contains(t, idx, idA)
}

func TestAppendIndexIsSafeUnderConcurrency(t *testing.T) {
	root := t.TempDir()
	s, err := newDiskStore(root)
	require.NoError(t, err)
	day := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	require.NoError(t, os.MkdirAll(filepath.Join(root, dirRaw, "2026-10-09"), 0o755))
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, s.appendIndex(day, IndexEntry{ExchangeID: fmt.Sprintf("ex-%d", i), ClientPath: strings.Repeat("p", 5000)}))
		}(i)
	}
	wg.Wait()
	raw := readFile(t, root, "raw/2026-10-09/index.ndjson")
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	require.Len(t, lines, 50)
	for _, line := range lines {
		var e IndexEntry
		require.NoError(t, json.Unmarshal([]byte(line), &e), "every index line must be intact JSON")
	}
}

func TestBodyFileTruncatesAndCountsAllBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bin")
	f, err := createBodyFile(path, 5)
	require.NoError(t, err)
	f.write([]byte("abc"))
	f.write([]byte("defg"))
	f.write([]byte("h"))
	require.NoError(t, f.close())
	require.True(t, f.truncated)
	require.Equal(t, int64(8), f.n)
	got, _ := os.ReadFile(path)
	require.Equal(t, "abcde", string(got))
	f.write([]byte("ignored after close"))
	require.NoError(t, f.close())
}

func TestCommitFailureDegradesRecorder(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	ex, _, finish := startExchange(t, rec)
	// 让 raw 目录不可写：把 raw 换成普通文件。
	require.NoError(t, os.RemoveAll(filepath.Join(rec.Root(), dirRaw)))
	require.NoError(t, os.WriteFile(filepath.Join(rec.Root(), dirRaw), []byte("x"), 0o644))
	finish()
	rec.Flush()
	require.True(t, rec.Stats().Degraded)
	require.False(t, rec.active())
	_, err := os.Stat(rec.store.spoolDir(ex.ID()))
	require.NoError(t, err, "exchange stays in spool for recovery on next start")
}

func TestRecoverSpoolKeepsStatusWhenFinalMetaWasWritten(t *testing.T) {
	root := t.TempDir()
	id := uuid.Must(uuid.NewV7()).String()
	dir := filepath.Join(root, dirSpool, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	finished := time.Date(2026, 10, 9, 8, 0, 1, 0, time.UTC)
	m := Meta{FormatVersion: 1, ExchangeID: id, Status: StatusComplete, UserID: 4,
		StartedAt: time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC), FinishedAt: &finished}
	require.NoError(t, writeJSONAtomic(filepath.Join(dir, fileMeta), m))

	rec, err := New(Options{Root: root}, nil)
	require.NoError(t, err)
	defer rec.Close()
	got := readMeta(t, filepath.Join(root, "raw", "2026-10-09", "u4", id))
	require.Equal(t, StatusComplete, got.Status)
	require.True(t, got.Recovered)
	require.Equal(t, finished, *got.FinishedAt)
}

package trajectory

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/trajectory/ir"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

// Sessions / 回放 / 原始文件 / 下载（design.md §7）。

const (
	defaultRawLimit = 1 << 20
	maxRawLimit     = 16 << 20
)

var errIndexNotReady = errors.New("trajectory index is not available (no database or recorder disabled)")

func (r *Recorder) pgStore() (*pgIndexStore, error) {
	if ix := r.indexer.Load(); ix != nil {
		if s, ok := ix.store.(*pgIndexStore); ok {
			return s, nil
		}
	}
	return nil, errIndexNotReady
}

// SessionDetail GET /sessions/:id。
type SessionDetail struct {
	Session   *SessionRow   `json:"session"`
	Exchanges []ExchangeRow `json:"exchanges"`
}

// RawFile exchange 目录下的一个文件。
type RawFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// ExchangeDetail GET /exchanges/:id：索引行 + meta + 最终 attempt 的请求 / 响应 IR。
type ExchangeDetail struct {
	Row          *ExchangeRow `json:"row,omitempty"`
	Meta         *Meta        `json:"meta"`
	Dir          string       `json:"dir"`
	Files        []RawFile    `json:"files"`
	Request      *ir.Request  `json:"request,omitempty"`
	Response     *ir.Response `json:"response,omitempty"`
	MessagesFrom int          `json:"messages_from"`
	TotalMsgs    int          `json:"total_messages"`
	ParseError   string       `json:"parse_error,omitempty"`
}

func adminListSessions(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	st, err := rec.pgStore()
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	page, pageSize := response.ParsePagination(c)
	q := SessionQuery{Client: c.Query("client"), Model: c.Query("model"), Q: strings.TrimSpace(c.Query("q")), Page: page, PageSize: pageSize}
	if v, err := strconv.ParseInt(c.Query("user_id"), 10, 64); err == nil {
		q.UserID = v
	}
	if t, err := time.Parse(time.RFC3339, c.Query("from")); err == nil {
		q.From = &t
	}
	if t, err := time.Parse(time.RFC3339, c.Query("to")); err == nil {
		q.To = &t
	}
	items, total, err := st.listSessions(c.Request.Context(), q)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

func adminGetSession(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	st, err := rec.pgStore()
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	sess, rows, err := st.getSession(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	if sess == nil {
		response.NotFound(c, "session not found")
		return
	}
	response.Success(c, SessionDetail{Session: sess, Exchanges: rows})
}

// exchangeDir 定位 exchange 目录：优先索引，其次扫描每日 index.ndjson。
func (r *Recorder) exchangeDir(c *gin.Context, id string) (string, *ExchangeRow, error) {
	if !isExchangeID(id) {
		return "", nil, nil
	}
	if st, err := r.pgStore(); err == nil {
		row, err := st.getExchange(c.Request.Context(), id)
		if err != nil {
			return "", nil, err
		}
		if row != nil {
			return filepath.Join(r.opts.Root, filepath.FromSlash(row.RawPath)), row, nil
		}
	}
	days := indexDays(r.opts.Root)
	for i := len(days) - 1; i >= 0; i-- {
		entries, err := readIndexDesc(r.opts.Root, days[i])
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.ExchangeID == id {
				return filepath.Join(r.opts.Root, filepath.FromSlash(e.Path)), nil, nil
			}
		}
	}
	return "", nil, nil
}

// isExchangeID exchange id 是 UUID；拒绝其它输入，避免拼进路径。
func isExchangeID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if ch != '-' {
				return false
			}
		case (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f'):
		default:
			return false
		}
	}
	return true
}

func listRawFiles(dir string) []RawFile {
	var out []RawFile
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		out = append(out, RawFile{Path: filepath.ToSlash(rel), Bytes: info.Size()})
		return nil
	})
	slices.SortFunc(out, func(a, b RawFile) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func adminGetExchange(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	dir, row, err := rec.exchangeDir(c, c.Param("id"))
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	if dir == "" {
		response.NotFound(c, "exchange not found")
		return
	}
	meta, err := loadMetaFile(dir)
	if err != nil {
		response.InternalError(c, "read meta.json: "+err.Error())
		return
	}
	d := ExchangeDetail{Row: row, Meta: meta, Dir: dir, Files: listRawFiles(dir)}
	p := parseExchange(dir, meta)
	if p.reqErr != nil {
		d.ParseError = p.reqErr.Error()
	}
	d.Request, d.Response = p.req, p.resp
	if d.Request != nil {
		d.TotalMsgs = len(d.Request.Messages)
		from, _ := strconv.Atoi(c.Query("messages_from"))
		from = max(0, min(from, d.TotalMsgs))
		d.MessagesFrom = from
		d.Request.Messages = d.Request.Messages[from:]
		if from > 0 && c.Query("include_system") != "1" {
			d.Request.System, d.Request.Tools = nil, nil
		}
	}
	response.Success(c, d)
}

// adminGetRaw 分段读取 exchange 目录下的原始文件：?file=client.req.json&offset=0&limit=1048576。
func adminGetRaw(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	dir, _, err := rec.exchangeDir(c, c.Param("id"))
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	if dir == "" {
		response.NotFound(c, "exchange not found")
		return
	}
	name := c.Query("file")
	files := listRawFiles(dir)
	idx := slices.IndexFunc(files, func(f RawFile) bool { return f.Path == name })
	if idx < 0 {
		response.NotFound(c, "file not found in exchange")
		return
	}
	offset, _ := strconv.ParseInt(c.Query("offset"), 10, 64)
	limit, _ := strconv.ParseInt(c.Query("limit"), 10, 64)
	if limit <= 0 {
		limit = defaultRawLimit
	}
	limit = min(limit, maxRawLimit)
	offset = max(0, min(offset, files[idx].Bytes))
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	n := min(limit, files[idx].Bytes-offset)
	c.Header("X-Total-Size", strconv.FormatInt(files[idx].Bytes, 10))
	c.Header("X-Offset", strconv.FormatInt(offset, 10))
	c.Header("Cache-Control", "no-store")
	if c.Query("download") == "1" {
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.Param("id")+"-"+strings.ReplaceAll(name, "/", "_")))
	}
	c.DataFromReader(http.StatusOK, n, "text/plain; charset=utf-8", io.LimitReader(f, n), nil)
}

// adminDownloadSession 打包 session 的所有 exchange 目录与索引行为 .tar.zst（流式输出）。
func adminDownloadSession(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	st, err := rec.pgStore()
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	sess, rows, err := st.getSession(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	if sess == nil {
		response.NotFound(c, "session not found")
		return
	}
	name := "session-" + sess.SessionID
	c.Header("Content-Type", "application/zstd")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name+".tar.zst"))
	c.Status(http.StatusOK)
	if err := writeSessionArchive(c.Writer, name, rec.opts.Root, sess, rows); err != nil {
		_ = c.Error(err)
	}
}

func writeSessionArchive(w io.Writer, prefix, root string, sess *SessionRow, rows []ExchangeRow) error {
	zw, err := zstd.NewWriter(w)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	index, _ := json.MarshalIndent(SessionDetail{Session: sess, Exchanges: rows}, "", "  ")
	now := time.Now()
	if err := tw.WriteHeader(&tar.Header{Name: prefix + "/session.json", Mode: 0o644, Size: int64(len(index)), ModTime: now}); err != nil {
		return err
	}
	if _, err := tw.Write(index); err != nil {
		return err
	}
	for _, row := range rows {
		dir := filepath.Join(root, filepath.FromSlash(row.RawPath))
		for _, f := range listRawFiles(dir) {
			if err := addTarFile(tw, filepath.Join(dir, filepath.FromSlash(f.Path)), prefix+"/"+row.RawPath+"/"+f.Path); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func addTarFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func adminReindex(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	ix := rec.Indexer()
	if ix == nil {
		response.Error(c, http.StatusServiceUnavailable, errIndexNotReady.Error())
		return
	}
	response.Success(c, gin.H{"started": ix.Rebuild()})
}

const maxSessionLabelRunes = 200

// adminSetSessionLabel PUT /sessions/:id/label {"label": "..."}；空串清除自定义名称。
func adminSetSessionLabel(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	st, err := rec.pgStore()
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "invalid request body: "+err.Error())
		return
	}
	label := strings.TrimSpace(body.Label)
	if utf8.RuneCountInString(label) > maxSessionLabelRunes {
		response.BadRequest(c, fmt.Sprintf("label is longer than %d characters", maxSessionLabelRunes))
		return
	}
	id := c.Param("id")
	sess, _, err := st.getSession(c.Request.Context(), id)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	if sess == nil {
		response.NotFound(c, "session not found")
		return
	}
	if err := st.setSessionLabel(c.Request.Context(), id, label); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, gin.H{"session_id": id, "label": label})
}

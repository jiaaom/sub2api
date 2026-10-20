package trajectory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Exchange 一次客户端请求的录制句柄。
//
// 生命周期：middleware 创建（此时尚未决定是否录制）→ 首次需要时（第一次上游调用、第一次写客户端响应或
// middleware 结束）调用 Recording() 决定 → middleware 结束并且所有 attempt 关闭后异步落定。
// 决定之前 A 只暂存在内存中；决定不录则什么都不落盘。
type Exchange struct {
	rec    *Recorder
	ginCtx *gin.Context // 仅在 decideOnce 内、且 middleware 返回前使用
	// clientCtx 客户端请求的原始 context：客户端断开时被取消，用于区分 closed_early 与 aborted。
	// 不能用上游请求自己的 ctx——部分 service 读到终止事件后会先 cancel 上游 ctx 再关闭 body。
	clientCtx context.Context
	skip      bool
	id        string
	started   time.Time

	clientReq          []byte
	clientReqTruncated bool
	clientReqErr       error

	decideOnce sync.Once
	recording  bool

	mu          sync.Mutex
	dir         string
	meta        Meta
	refs        int
	released    bool
	finalized   bool
	openBodies  map[int]*teeBody
	io          sync.WaitGroup // 后台磁盘写入（A、B、初始 meta）
	clientResp  *asyncSink
	clientRespN string // 客户端响应文件名（相对 exchange 目录）
	panicked    bool
}

func (r *Recorder) newExchange(c *gin.Context, skip bool) *Exchange {
	now := r.opts.Now()
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	ex := &Exchange{
		rec:       r,
		ginCtx:    c,
		clientCtx: c.Request.Context(),
		skip:      skip,
		id:        id.String(),
		started:   now,
		refs:      1, // middleware 持有
	}
	req := c.Request
	ex.meta = Meta{
		FormatVersion: FormatVersion,
		ExchangeID:    ex.id,
		Status:        StatusInProgress,
		StartedAt:     now.UTC(),
		Attempts:      []AttemptMeta{},
		Client: ClientMeta{
			Method:    req.Method,
			Path:      req.URL.Path,
			Query:     redactQuery(req.URL.RawQuery),
			Protocol:  detectProtocol(req.URL.Path),
			Headers:   redactHeaders(req.Header),
			UserAgent: req.UserAgent(),
		},
	}
	ex.meta.Client.DetectedClient = detectClient(req.Header)
	return ex
}

// ID exchange id（UUIDv7）。
func (ex *Exchange) ID() string { return ex.id }

// captureClientRequest 读取 A 并原样放回 body。超过上限的部分不缓存，但下游仍能完整读到。
func (ex *Exchange) captureClientRequest(req *http.Request) {
	if req.Body == nil || req.Body == http.NoBody {
		return
	}
	limit := ex.rec.opts.MaxBodyBytes
	orig := req.Body
	buf, err := io.ReadAll(io.LimitReader(orig, limit+1))
	if err != nil {
		ex.clientReqErr = err
		req.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(buf), errReader{err}), Closer: orig}
		ex.clientReq = buf
		return
	}
	if int64(len(buf)) > limit {
		ex.clientReqTruncated = true
		req.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(buf), orig), Closer: orig}
		ex.clientReq = buf[:limit]
		return
	}
	_ = orig.Close()
	ex.clientReq = buf
	req.Body = io.NopCloser(bytes.NewReader(buf))
}

// Recording 返回本 exchange 是否录制；首次调用时做决定并初始化 spool 目录。
func (ex *Exchange) Recording() bool {
	if ex == nil {
		return false
	}
	ex.decideOnce.Do(ex.decide)
	return ex.recording
}

func (ex *Exchange) decide() {
	if ex.skip || ex.ginCtx == nil {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(ex.ginCtx)
	if !ok || apiKey == nil {
		return
	}
	var groupID int64
	if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	if !ex.rec.decide(groupID, apiKey.ID) {
		return
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	ex.meta.UserID = apiKey.UserID
	ex.meta.APIKeyID = apiKey.ID
	ex.meta.APIKeyName = apiKey.Name
	ex.meta.GroupID = groupID
	if apiKey.Group != nil {
		ex.meta.GroupName = apiKey.Group.Name
	}
	ex.dir = ex.rec.store.spoolDir(ex.id)
	if err := ex.openLocked(); err != nil {
		ex.rec.fail("open exchange", err)
		return
	}
	ex.recording = true
	ex.rec.stats.exchangesStarted.Add(1)
}

// openLocked 填充 meta 并把 A 与初始 meta.json 交给后台写入（不阻塞发往上游的请求）。
func (ex *Exchange) openLocked() error {
	m := &ex.meta
	// stream / session hint / model 等需要解析 body 的字段留到 finalize 再算，不占热路径。
	var reqName string
	if len(ex.clientReq) > 0 || ex.clientReqErr != nil {
		reqName = "client.req" + bodyExt(m.Client.Headers.Get("Content-Type"), ex.clientReq)
		m.Client.RequestFile = reqName
		m.Client.RequestBytes = int64(len(ex.clientReq))
		if int64(len(ex.clientReq)) > ex.rec.opts.MaxBodyBytes || ex.clientReqTruncated {
			m.Truncated = true
			m.Notes = append(m.Notes, "client request body truncated at max_body_bytes")
		}
		if ex.clientReqErr != nil {
			m.Notes = append(m.Notes, "client request body read error: "+ex.clientReqErr.Error())
		}
	}
	initialMeta, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dir, body, limit := ex.dir, ex.clientReq, ex.rec.opts.MaxBodyBytes
	ex.async("write client request", func() error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if reqName != "" {
			if _, err := writeWholeFile(filepath.Join(dir, reqName), body, limit); err != nil {
				return err
			}
		}
		return writeFileAtomic(filepath.Join(dir, fileMeta), append(initialMeta, '\n'))
	})
	return nil
}

// async 在后台执行磁盘写入；finalize 会先等待全部完成。
func (ex *Exchange) async(op string, fn func() error) {
	ex.io.Add(1)
	go func() {
		defer ex.io.Done()
		if err := fn(); err != nil {
			ex.rec.fail(op, err)
		}
	}()
}

// captureClientResponse D：tee 回给客户端的字节。
func (ex *Exchange) captureClientResponse(p []byte, header http.Header) {
	if !ex.Recording() {
		return
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	if ex.finalized {
		return
	}
	if ex.clientResp == nil {
		ex.clientRespN = "client.resp" + responseExt(header.Get("Content-Type"), ex.meta.Client.Headers.Get("Accept"))
		ex.clientResp = newAsyncSink(filepath.Join(ex.dir, ex.clientRespN), "", ex.rec.opts.MaxBodyBytes)
	}
	ex.clientResp.write(p, 0)
}

// finishFromGin 在 middleware 返回前调用（仍在请求 goroutine 内，gin.Context 有效）。
func (ex *Exchange) finishFromGin(c *gin.Context, panicked bool) {
	recording := ex.Recording()
	if recording {
		ex.mu.Lock()
		ctx := c.Request.Context()
		if id, _ := ctx.Value(ctxkey.ClientRequestID).(string); id != "" {
			ex.meta.Client.ClientRequestID = id
		}
		ex.meta.Client.StatusCode = c.Writer.Status()
		ex.meta.Client.ResponseHeaders = redactHeaders(c.Writer.Header())
		if ctx.Err() != nil {
			ex.meta.Client.Canceled = true
		}
		ex.panicked = panicked
		ex.mu.Unlock()
	}
	ex.ginCtx = nil
	ex.release()
}

// release middleware 放弃持有；若仍有未关闭的上游响应，超时后强制落定。
func (ex *Exchange) release() {
	ex.mu.Lock()
	ex.released = true
	ex.refs--
	done := ex.refs == 0
	ex.mu.Unlock()
	if done {
		ex.scheduleFinalize()
		return
	}
	if ex.recording {
		time.AfterFunc(ex.rec.opts.AbandonAfter, ex.abandon)
	}
}

func (ex *Exchange) abandon() {
	ex.mu.Lock()
	if ex.finalized {
		ex.mu.Unlock()
		return
	}
	bodies := make([]*teeBody, 0, len(ex.openBodies))
	for _, b := range ex.openBodies {
		bodies = append(bodies, b)
	}
	ex.mu.Unlock()
	for _, b := range bodies {
		b.end(OutcomeAbandoned, nil)
	}
}

func (ex *Exchange) scheduleFinalize() {
	if !ex.recording {
		return
	}
	ex.rec.pending.Add(1)
	go func() {
		defer ex.rec.pending.Done()
		ex.finalize()
	}()
}

func (ex *Exchange) finalize() {
	ex.io.Wait()
	ex.mu.Lock()
	if ex.finalized {
		ex.mu.Unlock()
		return
	}
	ex.finalized = true
	clientResp := ex.clientResp
	ex.mu.Unlock()

	clientResp.close()
	res := clientResp.wait()
	if res.err != nil {
		ex.rec.fail("write client response", res.err)
	}

	ex.mu.Lock()
	m := &ex.meta
	if res.wrote {
		m.Client.ResponseFile = ex.clientRespN
		m.Client.ResponseBytes = res.bytes
		if res.truncated {
			m.Truncated = true
		}
	}
	finished := ex.rec.opts.Now().UTC()
	m.FinishedAt = &finished
	m.Status = StatusComplete
	if ex.panicked {
		m.Status = StatusError
		m.Notes = append(m.Notes, "handler panicked")
	} else if m.Client.Canceled {
		// 客户端常在收到终止事件后立即断开（如 oh-my-pi 收到 response.completed 即关连接）：
		// 此时 D 已完整，canceled 仅作为事实保留。
		if !m.Truncated && responseFileTerminal(ex.dir, m.Client.Protocol, m.Client.ResponseFile) {
			m.Notes = append(m.Notes, "client disconnected after terminal event")
		} else {
			m.Status = StatusIncomplete
		}
	}
	for i := range m.Attempts {
		// 读到终止事件后才因取消/读错误结束的 attempt，数据已完整，按 closed_early 处理；原始错误保留在 error。
		if att := &m.Attempts[i]; (att.Outcome == OutcomeError || att.Outcome == OutcomeAborted) && !att.Truncated &&
			responseFileTerminal(ex.dir, att.UpstreamProtocol, att.ResponseFile) {
			m.Notes = append(m.Notes, fmt.Sprintf("attempt %d: terminal event received before %s; treated as %s", i, att.Outcome, OutcomeClosedEarly))
			att.Outcome = OutcomeClosedEarly
		}

		switch m.Attempts[i].Outcome {
		case OutcomeAborted, OutcomeAbandoned, OutcomePending:
			if m.Status == StatusComplete {
				m.Status = StatusIncomplete
			}
		}
		if o := m.Attempts[i].Outcome; o == OutcomeSuccess || o == OutcomeClosedEarly {
			idx := i
			m.FinalAttempt = &idx
		}
		if m.Attempts[i].Truncated {
			m.Truncated = true
		}
	}
	if m.Status == StatusComplete && m.FinalAttempt == nil && len(m.Attempts) > 0 {
		m.Status = StatusError
	}
	ex.mu.Unlock()

	m.Client.Stream = requestWantsStream(m.Client.Protocol, m.Client.Path, m.Client.Query, ex.clientReq)
	m.Client.SessionHint = detectSessionHint(m.Client.Headers, ex.clientReq)
	m.Model.Requested = requestedModel(m.Client.Protocol, m.Client.Path, ex.clientReq)
	summarizeExchange(ex.dir, m)
	rel, err := ex.rec.store.commit(ex.dir, m)
	if err != nil {
		ex.rec.fail("commit exchange", err)
		slog.Error("trajectory: commit exchange failed", "exchange_id", ex.id, "error", err)
		return
	}
	ex.rec.stats.exchangesFinalized.Add(1)
	if ix := ex.rec.indexer.Load(); ix != nil {
		ix.Enqueue(filepath.Join(ex.rec.opts.Root, rel), m)
	}
}

// beginAttempt B：记录发往上游的请求。返回 nil 表示不录。
func (ex *Exchange) beginAttempt(req *http.Request, accountID int64) *Attempt {
	if !ex.Recording() {
		return nil
	}
	ex.mu.Lock()
	if ex.finalized || (ex.released && ex.refs == 0) {
		ex.mu.Unlock()
		ex.rec.stats.lateAttempts.Add(1)
		return nil
	}
	index := len(ex.meta.Attempts)
	ex.refs++
	am := AttemptMeta{
		Index:            index,
		AccountID:        accountID,
		UpstreamProtocol: detectProtocol(req.URL.Path),
		Method:           req.Method,
		URL:              redactURL(req.URL),
		RequestHeaders:   redactHeaders(req.Header),
		StartedAt:        ex.rec.opts.Now().UTC(),
		Outcome:          OutcomePending,
	}
	if platform, _ := req.Context().Value(ctxkey.Platform).(string); platform != "" {
		am.Platform = platform
	}
	ex.meta.Attempts = append(ex.meta.Attempts, am)
	ex.mu.Unlock()
	ex.rec.stats.attempts.Add(1)

	att := &Attempt{ex: ex, index: index, dir: filepath.Join(ex.dir, dirAttempt, strconv.Itoa(index)), start: time.Now()}
	body, readErr := readAndRestoreRequestBody(req)
	name := "upstream.req" + bodyExt(req.Header.Get("Content-Type"), body)
	limit := ex.rec.opts.MaxBodyBytes
	truncated := int64(len(body)) > limit
	reqPath := filepath.Join(att.dir, name)
	ex.async("write upstream request", func() error {
		_, err := writeWholeFile(reqPath, body, limit)
		return err
	})
	ex.updateAttempt(index, func(a *AttemptMeta) {
		a.RequestFile = filepath.ToSlash(filepath.Join(dirAttempt, strconv.Itoa(index), name))
		a.BytesReq = int64(len(body))
		a.Truncated = a.Truncated || truncated
		if readErr != nil {
			a.Error = "request body read error: " + readErr.Error()
		}
	})
	return att
}

func (ex *Exchange) updateAttempt(index int, fn func(*AttemptMeta)) {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	if index < len(ex.meta.Attempts) {
		fn(&ex.meta.Attempts[index])
	}
}

func (ex *Exchange) endAttempt() {
	ex.mu.Lock()
	ex.refs--
	done := ex.released && ex.refs == 0
	ex.mu.Unlock()
	if done {
		ex.scheduleFinalize()
	}
}

// readAndRestoreRequestBody 读出请求 body 并放回，同时设置 GetBody 以便 net/http 重试。
func readAndRestoreRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	orig := req.Body
	body, err := io.ReadAll(orig)
	_ = orig.Close()
	if err != nil {
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), errReader{err}))
		return body, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return body, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

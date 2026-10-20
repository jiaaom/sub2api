package trajectory

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// WrapHTTPUpstream 装饰 service.HTTPUpstream，录制 B（上游请求）与 C（上游响应字节流）。
// rec 为 nil 时原样返回 inner。
func WrapHTTPUpstream(inner service.HTTPUpstream, rec *Recorder) service.HTTPUpstream {
	if rec == nil || inner == nil {
		return inner
	}
	return &recordingUpstream{inner: inner, rec: rec}
}

type recordingUpstream struct {
	inner service.HTTPUpstream
	rec   *Recorder
}

func (u *recordingUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	att := u.begin(req, accountID)
	resp, err := u.inner.Do(req, proxyURL, accountID, accountConcurrency)
	return att.observe(resp, err)
}

func (u *recordingUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	att := u.begin(req, accountID)
	resp, err := u.inner.DoWithTLS(req, proxyURL, accountID, accountConcurrency, profile)
	return att.observe(resp, err)
}

func (u *recordingUpstream) begin(req *http.Request, accountID int64) *Attempt {
	if req == nil {
		return nil
	}
	ex := FromContext(req.Context())
	if ex == nil {
		u.rec.noteUnattributed(req)
		return nil
	}
	return ex.beginAttempt(req, accountID)
}

// Attempt 一次上游尝试的录制句柄。nil 安全。
type Attempt struct {
	ex    *Exchange
	index int
	dir   string
	start time.Time
}

func (a *Attempt) observe(resp *http.Response, err error) (*http.Response, error) {
	if a == nil {
		return resp, err
	}
	elapsed := time.Since(a.start).Milliseconds()
	if err != nil || resp == nil {
		a.ex.updateAttempt(a.index, func(m *AttemptMeta) {
			m.Outcome = OutcomeError
			m.DurationMs = elapsed
			if err != nil {
				m.Error = err.Error()
			}
		})
		a.ex.endAttempt()
		return resp, err
	}
	a.ex.updateAttempt(a.index, func(m *AttemptMeta) {
		m.StatusCode = resp.StatusCode
		m.ResponseHeaders = redactHeaders(resp.Header)
		m.TTFBMs = elapsed
	})
	if resp.Body == nil || resp.Body == http.NoBody {
		tb := &teeBody{att: a, status: resp.StatusCode}
		tb.end(outcomeForEOF(resp.StatusCode), nil)
		return resp, nil
	}
	resp.Body = a.wrapBody(resp)
	return resp, nil
}

func (a *Attempt) wrapBody(resp *http.Response) io.ReadCloser {
	ex := a.ex
	accept := ""
	if resp.Request != nil {
		accept = resp.Request.Header.Get("Accept")
	}
	name := "upstream.resp" + responseExt(resp.Header.Get("Content-Type"), accept)
	dataPath, timingPath := sinkPaths(a.dir, name)
	tb := &teeBody{
		rc:     resp.Body,
		att:    a,
		status: resp.StatusCode,
		name:   name,
		sink:   newAsyncSink(dataPath, timingPath, ex.rec.opts.MaxBodyBytes),
	}
	ex.mu.Lock()
	if ex.openBodies == nil {
		ex.openBodies = map[int]*teeBody{}
	}
	ex.openBodies[a.index] = tb
	ex.mu.Unlock()
	return tb
}

// teeBody 包装上游响应 body：调用方读多少就旁路拷贝多少交给 asyncSink，不改变字节、不碰磁盘。
type teeBody struct {
	rc     io.ReadCloser
	att    *Attempt
	status int
	name   string
	sink   *asyncSink

	mu    sync.Mutex
	ended bool
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		t.capture(p[:n])
	}
	if errors.Is(err, io.EOF) {
		t.end(outcomeForEOF(t.status), nil)
	} else if err != nil {
		t.end(OutcomeError, err)
	}
	return n, err
}

func (t *teeBody) Close() error {
	outcome := OutcomeClosedEarly
	if t.status < 200 || t.status >= 300 {
		outcome = OutcomeHTTPError
	} else if ctx := t.att.ex.clientCtx; ctx != nil && ctx.Err() != nil {
		outcome = OutcomeAborted
	}
	err := t.rc.Close()
	t.end(outcome, nil)
	return err
}

func (t *teeBody) capture(p []byte) {
	t.mu.Lock()
	ended := t.ended
	t.mu.Unlock()
	if !ended {
		t.sink.write(p, time.Since(t.att.start).Milliseconds())
	}
}

// end 结束本次 attempt；只生效一次。落盘收尾在后台完成后才释放 exchange 引用。
func (t *teeBody) end(outcome string, err error) {
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.ended = true
	t.mu.Unlock()

	ex := t.att.ex
	index := t.att.index
	duration := time.Since(t.att.start).Milliseconds()
	ex.mu.Lock()
	delete(ex.openBodies, index)
	ex.mu.Unlock()

	t.sink.close()
	finish := func() {
		res := t.sink.wait()
		if res.err != nil {
			ex.rec.fail("write upstream response", res.err)
		}
		ex.updateAttempt(index, func(m *AttemptMeta) {
			m.Outcome = outcome
			m.BytesResp = res.bytes
			m.DurationMs = duration
			m.Truncated = m.Truncated || res.truncated
			if t.name != "" && res.wrote {
				m.ResponseFile = filepath.ToSlash(filepath.Join(dirAttempt, strconv.Itoa(index), t.name))
			}
			if err != nil {
				m.Error = err.Error()
			}
		})
		ex.endAttempt()
	}
	if t.sink == nil {
		finish()
		return
	}
	// 计入 pending：Flush / Close 需要等到落盘收尾与随后的落定都完成。
	ex.rec.pending.Add(1)
	go func() {
		defer ex.rec.pending.Done()
		finish()
	}()
}

func outcomeForEOF(status int) string {
	if status >= 200 && status < 300 {
		return OutcomeSuccess
	}
	return OutcomeHTTPError
}

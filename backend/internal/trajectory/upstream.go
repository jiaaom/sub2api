package trajectory

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"os"
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
	name := "upstream.resp" + responseExt(resp.Header.Get("Content-Type"))
	tb := &teeBody{rc: resp.Body, att: a, status: resp.StatusCode, name: name}
	f, err := createBodyFile(filepath.Join(a.dir, name), ex.rec.opts.MaxBodyBytes)
	if err != nil {
		ex.rec.fail("create upstream response file", err)
	} else {
		tb.file = f
	}
	if tf, err := os.OpenFile(filepath.Join(a.dir, "upstream.timing.ndjson"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		tb.timingFile = tf
		tb.timing = bufio.NewWriterSize(tf, 16<<10)
	} else {
		ex.rec.fail("create timing file", err)
	}
	ex.mu.Lock()
	if ex.openBodies == nil {
		ex.openBodies = map[int]*teeBody{}
	}
	ex.openBodies[a.index] = tb
	ex.mu.Unlock()
	return tb
}

// teeBody 包装上游响应 body：调用方读多少就旁路写多少，不改变字节、不增加延迟。
type teeBody struct {
	rc     io.ReadCloser
	att    *Attempt
	status int
	name   string

	mu         sync.Mutex
	file       *bodyFile
	timingFile *os.File
	timing     *bufio.Writer
	off        int64
	ended      bool
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
	defer t.mu.Unlock()
	if t.ended {
		return
	}
	if t.timing != nil {
		ms := time.Since(t.att.start).Milliseconds()
		line := make([]byte, 0, 48)
		line = append(line, `{"off":`...)
		line = strconv.AppendInt(line, t.off, 10)
		line = append(line, `,"n":`...)
		line = strconv.AppendInt(line, int64(len(p)), 10)
		line = append(line, `,"t":`...)
		line = strconv.AppendInt(line, ms, 10)
		line = append(line, "}\n"...)
		_, _ = t.timing.Write(line)
	}
	t.off += int64(len(p))
	t.file.write(p)
}

// end 结束本次 attempt；只生效一次。
func (t *teeBody) end(outcome string, err error) {
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.ended = true
	ex := t.att.ex
	var ioErr error
	if t.file != nil {
		ioErr = t.file.close()
	}
	if t.timing != nil {
		ioErr = errors.Join(ioErr, t.timing.Flush(), t.timingFile.Close())
	}
	bytesResp := t.off
	truncated := t.file != nil && t.file.truncated
	t.mu.Unlock()

	if ioErr != nil {
		ex.rec.fail("write upstream response", ioErr)
	}
	duration := time.Since(t.att.start).Milliseconds()
	index := t.att.index
	ex.updateAttempt(index, func(m *AttemptMeta) {
		m.Outcome = outcome
		m.BytesResp = bytesResp
		m.DurationMs = duration
		m.Truncated = m.Truncated || truncated
		if t.name != "" && t.file != nil {
			m.ResponseFile = filepath.ToSlash(filepath.Join(dirAttempt, strconv.Itoa(index), t.name))
		}
		if err != nil {
			m.Error = err.Error()
		}
	})
	ex.mu.Lock()
	delete(ex.openBodies, index)
	ex.mu.Unlock()
	ex.endAttempt()
}

func outcomeForEOF(status int) string {
	if status >= 200 && status < 300 {
		return OutcomeSuccess
	}
	return OutcomeHTTPError
}

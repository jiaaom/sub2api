package trajectory

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// startExchange 构造一个已决定录制的 exchange（绕过 HTTP，直接测装饰器）。
func startExchange(t *testing.T, rec *Recorder) (*Exchange, context.Context, func()) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude"}`))
	c.Set("api_key", testAPIKey())
	ex := rec.newExchange(c, false)
	ex.captureClientRequest(c.Request)
	require.True(t, ex.Recording())
	ctx := WithExchange(context.Background(), ex)
	return ex, ctx, func() { ex.finishFromGin(c, false) }
}

func TestWrapHTTPUpstreamRestoresRequestBodyAndGetBody(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	var getBodyContent string
	fake := &fakeUpstream{respond: func(req *http.Request) (*http.Response, error) {
		require.NotNil(t, req.GetBody)
		rc, err := req.GetBody()
		require.NoError(t, err)
		b, _ := io.ReadAll(rc)
		getBodyContent = string(b)
		return textResponse(200, "application/json", `{"ok":true}`), nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	_, ctx, finish := startExchange(t, rec)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.example/v1/messages", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := up.DoWithTLS(req, "", 1, 1, nil)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	finish()

	require.Equal(t, `{"ok":true}`, string(body))
	require.Equal(t, `{"x":1}`, string(fake.bodies[0]))
	require.Equal(t, `{"x":1}`, getBodyContent)
	require.Equal(t, 1, fake.tlsCalls)
	dir, m := onlyExchange(t, rec)
	require.Equal(t, `{"x":1}`, readFile(t, dir, m.Attempts[0].RequestFile))
	require.Equal(t, `{"ok":true}`, readFile(t, dir, m.Attempts[0].ResponseFile))
}

// slowBody 逐块吐数据，用于验证 tee 是边读边写的流式行为。
type slowBody struct {
	chunks []string
	i      int
	closed bool
}

func (s *slowBody) Read(p []byte) (int, error) {
	if s.i >= len(s.chunks) {
		return 0, io.EOF
	}
	n := copy(p, s.chunks[s.i])
	s.i++
	return n, nil
}

func (s *slowBody) Close() error { s.closed = true; return nil }

func TestWrapHTTPUpstreamStreamsAndRecordsTiming(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	chunks := []string{"data: a\n\n", "data: b\n\n", "data: c\n\n"}
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		h := http.Header{"Content-Type": {"text/event-stream"}}
		return &http.Response{StatusCode: 200, Header: h, Body: &slowBody{chunks: chunks}}, nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	_, ctx, finish := startExchange(t, rec)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.example/v1/messages", strings.NewReader("{}"))
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(t, err)

	buf := make([]byte, 64)
	var got strings.Builder
	for {
		n, err := resp.Body.Read(buf)
		_, _ = got.Write(buf[:n])
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	require.NoError(t, resp.Body.Close())
	finish()

	require.Equal(t, strings.Join(chunks, ""), got.String())
	dir, m := onlyExchange(t, rec)
	require.Equal(t, strings.Join(chunks, ""), readFile(t, dir, m.Attempts[0].ResponseFile))
	timing := strings.Split(strings.TrimSpace(readFile(t, dir, "attempts/0/upstream.timing.ndjson")), "\n")
	require.Len(t, timing, 3)
	require.Contains(t, timing[1], `"off":9,"n":9`)
	require.Equal(t, OutcomeSuccess, m.Attempts[0].Outcome)
}

func TestWrapHTTPUpstreamEarlyCloseWithoutCancelIsComplete(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		h := http.Header{"Content-Type": {"text/event-stream"}}
		return &http.Response{StatusCode: 200, Header: h, Body: &slowBody{chunks: []string{"data: done\n\n", "data: trailing\n\n"}}}, nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	_, ctx, finish := startExchange(t, rec)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.example/v1/responses", strings.NewReader("{}"))
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(t, err)
	buf := make([]byte, 64)
	_, _ = resp.Body.Read(buf) // 读到终止事件后主动关闭
	require.NoError(t, resp.Body.Close())
	finish()

	_, m := onlyExchange(t, rec)
	require.Equal(t, OutcomeClosedEarly, m.Attempts[0].Outcome)
	require.Equal(t, StatusComplete, m.Status)
	require.NotNil(t, m.FinalAttempt)
}

func TestWrapHTTPUpstreamEarlyCloseMarksAborted(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		h := http.Header{"Content-Type": {"text/event-stream"}}
		return &http.Response{StatusCode: 200, Header: h, Body: &slowBody{chunks: []string{"data: a\n\n", "data: b\n\n"}}}, nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	ex, ctx, finish := startExchange(t, rec)
	clientCtx, cancel := context.WithCancel(context.Background())
	ex.clientCtx = clientCtx
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.example/v1/messages", strings.NewReader("{}"))
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(t, err)
	buf := make([]byte, 64)
	_, _ = resp.Body.Read(buf)
	cancel() // 客户端断开
	require.NoError(t, resp.Body.Close())
	finish()

	dir, m := onlyExchange(t, rec)
	require.Equal(t, OutcomeAborted, m.Attempts[0].Outcome)
	require.Equal(t, StatusIncomplete, m.Status)
	require.Equal(t, "data: a\n\n", readFile(t, dir, m.Attempts[0].ResponseFile))
}

func TestWrapHTTPUpstreamTransportErrorRecorded(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: refused") }}
	up := WrapHTTPUpstream(fake, rec)
	_, ctx, finish := startExchange(t, rec)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.example/v1/messages", strings.NewReader("{}"))
	_, err := up.Do(req, "", 1, 1)
	require.Error(t, err)
	finish()

	_, m := onlyExchange(t, rec)
	require.Equal(t, OutcomeError, m.Attempts[0].Outcome)
	require.Contains(t, m.Attempts[0].Error, "refused")
	require.Nil(t, m.FinalAttempt)
}

func TestExchangeWaitsForOpenAttemptsBeforeFinalizing(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		return textResponse(200, "application/json", `{"late":true}`), nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	_, ctx, finish := startExchange(t, rec)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.example/v1/messages", strings.NewReader("{}"))
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(t, err)

	finish() // middleware 先结束，响应 body 尚未读完
	rec.Flush()
	require.Empty(t, rawExchanges(t, rec.Root()), "must not finalize while an upstream body is still open")

	_, _ = io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	dir, m := onlyExchange(t, rec)
	require.Equal(t, `{"late":true}`, readFile(t, dir, m.Attempts[0].ResponseFile))
	require.Equal(t, StatusComplete, m.Status)

	// 已落定后再发生的上游调用记为 late，不写入已落定的目录。
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.example/v1/messages", strings.NewReader("{}"))
	resp2, err := up.Do(req2, "", 1, 1)
	require.NoError(t, err)
	_ = resp2.Body.Close()
	require.Equal(t, int64(1), rec.Stats().LateAttempts)
}

func TestExchangeAbandonsBodiesNeverClosed(t *testing.T) {
	rec, err := New(Options{Root: t.TempDir(), AbandonAfter: 20 * time.Millisecond}, nil)
	require.NoError(t, err)
	t.Cleanup(rec.Close)
	rec.SetScopes(enabledScopes())
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		return textResponse(200, "application/json", `{}`), nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	_, ctx, finish := startExchange(t, rec)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://u.example/v1/messages", strings.NewReader("{}"))
	_, err = up.Do(req, "", 1, 1) // 故意不读不关
	require.NoError(t, err)
	finish()

	require.Eventually(t, func() bool {
		rec.Flush()
		return len(rawExchanges(t, rec.Root())) == 1
	}, 2*time.Second, 10*time.Millisecond)
	_, m := onlyExchange(t, rec)
	require.Equal(t, OutcomeAbandoned, m.Attempts[0].Outcome)
	require.Equal(t, StatusIncomplete, m.Status)
}

func TestUnattributedUpstreamCallsAreCounted(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		return textResponse(200, "application/json", `{}`), nil
	}}
	up := WrapHTTPUpstream(fake, rec)

	gatewayCtx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "crid-9")
	req, _ := http.NewRequestWithContext(gatewayCtx, http.MethodPost, "https://u.example/v1/messages", strings.NewReader("{}"))
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(t, err)
	_ = resp.Body.Close()

	bg, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://u.example/usage", nil)
	resp, err = up.Do(bg, "", 1, 1)
	require.NoError(t, err)
	_ = resp.Body.Close()

	s := rec.Stats()
	require.Equal(t, int64(1), s.OrphanCalls)
	require.Equal(t, int64(1), s.BackgroundCalls)

	rec.SetScopes(nil) // 全局关闭时不计数
	resp, err = up.Do(req, "", 1, 1)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, int64(1), rec.Stats().OrphanCalls)
}

func TestWrapHTTPUpstreamNilRecorderReturnsInner(t *testing.T) {
	fake := &fakeUpstream{}
	wrapped, ok := WrapHTTPUpstream(fake, nil).(*fakeUpstream)
	require.True(t, ok)
	require.Same(t, fake, wrapped)
}

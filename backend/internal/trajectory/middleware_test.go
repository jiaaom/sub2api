package trajectory

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	clientBody   = `{"model":"gpt-5.5","stream":true,"input":"hi","prompt_cache_key":"sess-1"}`
	upstreamBody = `{"model":"gpt-5.5-2026","stream":true,"input":"hi","previous_response_id":"resp_prev"}`
	upstreamSSE  = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.5-2026\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.5-2026\",\"status\":\"completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"input_tokens_details\":{\"cached_tokens\":64},\"output_tokens_details\":{\"reasoning_tokens\":3}}}}\n\n"
)

// proxyHandler 模拟网关 handler：读客户端 body、改写后经 HTTPUpstream 发出，再把上游流转发给客户端。
func proxyHandler(up service.HTTPUpstream, rewritten string, attempts int) gin.HandlerFunc {
	return func(c *gin.Context) {
		got, err := io.ReadAll(c.Request.Body)
		if err != nil || len(got) == 0 {
			c.String(http.StatusBadRequest, "no body")
			return
		}
		var resp *http.Response
		for i := 0; i < attempts; i++ {
			req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://api.example.com/v1/responses?key=SECRETSECRET1234", strings.NewReader(rewritten))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer sk-upstream-abcdefgh9999")
			resp, err = up.Do(req, "", 7+int64(i), 1)
			if err != nil {
				c.String(http.StatusBadGateway, err.Error())
				return
			}
			if i < attempts-1 {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
		defer func() { _ = resp.Body.Close() }()
		c.Header("Content-Type", resp.Header.Get("Content-Type"))
		c.Status(resp.StatusCode)
		_, _ = io.Copy(c.Writer, resp.Body)
	}
}

func newGatewayEngine(rec *Recorder, up service.HTTPUpstream, key *service.APIKey, handler gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	g := r.Group("/v1")
	g.Use(GinMiddleware())
	g.Use(func(c *gin.Context) { // 模拟 clientRequestID middleware
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, "crid-1"))
		c.Next()
	})
	g.Use(fakeAuth(key))
	g.POST("/responses", handler)
	g.GET("/responses", handler)
	return r
}

func postJSON(r http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex_cli_rs/0.50")
	req.Header.Set("Authorization", "Bearer sk-client-key-123456")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMiddlewareRecordsAllFourPoints(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	useDefault(t, rec)
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		return textResponse(200, "text/event-stream", upstreamSSE), nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	engine := newGatewayEngine(rec, up, testAPIKey(), proxyHandler(up, upstreamBody, 1))

	w := postJSON(engine, "/v1/responses", clientBody)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, upstreamSSE, w.Body.String(), "client must receive upstream bytes unchanged")
	require.Equal(t, upstreamBody, string(fake.bodies[0]), "upstream must receive the full request body")

	dir, m := onlyExchange(t, rec)
	require.Contains(t, dir, "/u12/")
	require.Equal(t, StatusComplete, m.Status)
	require.Equal(t, testUserID, m.UserID)
	require.Equal(t, testKeyID, m.APIKeyID)
	require.Equal(t, testGroupID, m.GroupID)
	require.Equal(t, "coding", m.GroupName)
	require.Equal(t, "crid-1", m.Client.ClientRequestID)
	require.Equal(t, ProtocolOpenAIResponses, m.Client.Protocol)
	require.True(t, m.Client.Stream)
	require.Equal(t, "codex", m.Client.DetectedClient)
	require.Equal(t, &SessionHint{Source: "body:prompt_cache_key", Value: "sess-1"}, m.Client.SessionHint)
	require.Equal(t, http.StatusOK, m.Client.StatusCode)
	require.Equal(t, "Bearer ***3456", m.Client.Headers.Get("Authorization"))

	require.Equal(t, clientBody, readFile(t, dir, m.Client.RequestFile))
	require.Equal(t, upstreamSSE, readFile(t, dir, m.Client.ResponseFile))
	require.Equal(t, "client.resp.sse", m.Client.ResponseFile)

	require.Len(t, m.Attempts, 1)
	a := m.Attempts[0]
	require.Equal(t, OutcomeSuccess, a.Outcome)
	require.Equal(t, int64(7), a.AccountID)
	require.Equal(t, ProtocolOpenAIResponses, a.UpstreamProtocol)
	require.Equal(t, "Bearer ***9999", a.RequestHeaders.Get("Authorization"))
	require.NotContains(t, a.URL, "SECRETSECRET")
	require.Equal(t, upstreamBody, readFile(t, dir, a.RequestFile))
	require.Equal(t, upstreamSSE, readFile(t, dir, a.ResponseFile))
	require.Equal(t, int64(len(upstreamSSE)), a.BytesResp)
	require.Contains(t, readFile(t, dir, "attempts/0/upstream.timing.ndjson"), `"off":0`)

	// 1.8 摘要
	require.NotNil(t, m.FinalAttempt)
	require.Equal(t, 0, *m.FinalAttempt)
	require.Equal(t, "gpt-5.5", m.Model.Requested)
	require.Equal(t, "gpt-5.5-2026", m.Model.Upstream)
	require.Equal(t, "resp_1", m.ResponseID)
	require.Equal(t, "resp_prev", m.PreviousResponseID)
	require.Equal(t, &Usage{Input: 100, Output: 7, CacheRead: 64, Reasoning: 3}, m.Usage)

	// 索引行
	idx := readFile(t, rec.Root(), "raw/"+m.StartedAt.UTC().Format("2006-01-02")+"/index.ndjson")
	require.Contains(t, idx, m.ExchangeID)
	require.Equal(t, int64(1), rec.Stats().ExchangesFinalized)
	require.Zero(t, rec.Stats().OrphanCalls)
}

func TestMiddlewareRecordsEachFailoverAttempt(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	useDefault(t, rec)
	calls := 0
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return textResponse(529, "application/json", `{"error":"overloaded"}`), nil
		}
		return textResponse(200, "text/event-stream", upstreamSSE), nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	engine := newGatewayEngine(rec, up, testAPIKey(), proxyHandler(up, upstreamBody, 2))
	require.Equal(t, http.StatusOK, postJSON(engine, "/v1/responses", clientBody).Code)

	dir, m := onlyExchange(t, rec)
	require.Len(t, m.Attempts, 2)
	require.Equal(t, OutcomeHTTPError, m.Attempts[0].Outcome)
	require.Equal(t, 529, m.Attempts[0].StatusCode)
	require.Equal(t, int64(7), m.Attempts[0].AccountID)
	require.Equal(t, `{"error":"overloaded"}`, readFile(t, dir, m.Attempts[0].ResponseFile))
	require.Equal(t, OutcomeSuccess, m.Attempts[1].Outcome)
	require.Equal(t, int64(8), m.Attempts[1].AccountID)
	require.Equal(t, 1, *m.FinalAttempt)
	require.Equal(t, StatusComplete, m.Status)
}

func TestMiddlewareDoesNotRecordWhenScopeDisabled(t *testing.T) {
	for name, scopes := range map[string][]Scope{
		"global off":   {{Type: ScopeGroup, ID: testGroupID, Enabled: true}},
		"group off":    {{Type: ScopeGlobal, Enabled: true}},
		"key override": append(enabledScopes(), Scope{Type: ScopeAPIKey, ID: testKeyID, Enabled: false}),
	} {
		t.Run(name, func(t *testing.T) {
			rec := newTestRecorder(t, scopes)
			useDefault(t, rec)
			fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
				return textResponse(200, "text/event-stream", upstreamSSE), nil
			}}
			up := WrapHTTPUpstream(fake, rec)
			engine := newGatewayEngine(rec, up, testAPIKey(), proxyHandler(up, upstreamBody, 1))
			w := postJSON(engine, "/v1/responses", clientBody)
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, upstreamSSE, w.Body.String())
			rec.Flush()
			require.Empty(t, rawExchanges(t, rec.Root()))
			require.Zero(t, rec.Stats().OrphanCalls, "disabled scope must not be reported as a coverage gap")
		})
	}
}

func TestMiddlewareSkipsUnauthenticatedAndGETRequests(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	useDefault(t, rec)
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		return textResponse(200, "application/json", `{}`), nil
	}}
	up := WrapHTTPUpstream(fake, rec)

	unauth := newGatewayEngine(rec, up, nil, proxyHandler(up, upstreamBody, 1))
	require.Equal(t, http.StatusOK, postJSON(unauth, "/v1/responses", clientBody).Code)

	authed := newGatewayEngine(rec, up, testAPIKey(), func(c *gin.Context) {
		req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, "https://api.example.com/v1/models", nil)
		resp, err := up.Do(req, "", 1, 1)
		require.NoError(t, err)
		_ = resp.Body.Close()
		c.String(http.StatusOK, "ok")
	})
	w := httptest.NewRecorder()
	authed.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	require.Equal(t, http.StatusOK, w.Code)

	rec.Flush()
	require.Empty(t, rawExchanges(t, rec.Root()))
	require.Zero(t, rec.Stats().OrphanCalls)
}

func TestMiddlewareMarksPanicAsError(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	useDefault(t, rec)
	r := gin.New()
	r.Use(gin.CustomRecovery(func(c *gin.Context, _ any) { c.AbortWithStatus(http.StatusInternalServerError) }))
	g := r.Group("/v1", GinMiddleware(), fakeAuth(testAPIKey()))
	g.POST("/messages", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString("data: partial\n\n")
		panic("boom")
	})
	w := postJSON(r, "/v1/messages", `{"model":"claude"}`)
	require.Equal(t, http.StatusOK, w.Code) // 已经写出了 200 头

	dir, m := onlyExchange(t, rec)
	require.Equal(t, StatusError, m.Status)
	require.Equal(t, ProtocolAnthropicMessages, m.Client.Protocol)
	require.Equal(t, "data: partial\n\n", readFile(t, dir, m.Client.ResponseFile))
}

func TestMiddlewareTruncatesOversizedBodiesButForwardsThemIntact(t *testing.T) {
	rec, err := New(Options{Root: t.TempDir(), MaxBodyBytes: 16}, nil)
	require.NoError(t, err)
	t.Cleanup(rec.Close)
	rec.SetScopes(enabledScopes())
	useDefault(t, rec)

	big := `{"model":"m","input":"` + strings.Repeat("x", 100) + `"}`
	var seen []byte
	r := gin.New()
	g := r.Group("/v1", GinMiddleware(), fakeAuth(testAPIKey()))
	g.POST("/chat/completions", func(c *gin.Context) {
		seen, _ = io.ReadAll(c.Request.Body)
		c.String(http.StatusOK, strings.Repeat("y", 50))
	})
	require.Equal(t, http.StatusOK, postJSON(r, "/v1/chat/completions", big).Code)
	require.Equal(t, big, string(seen), "handler must still read the whole body")

	dir, m := onlyExchange(t, rec)
	require.True(t, m.Truncated)
	require.Equal(t, big[:16], readFile(t, dir, m.Client.RequestFile))
	require.Equal(t, strings.Repeat("y", 16), readFile(t, dir, m.Client.ResponseFile))
	require.Equal(t, int64(50), m.Client.ResponseBytes)
}

func TestAroundPlacesRecordingAtChainHead(t *testing.T) {
	rec := newTestRecorder(t, enabledScopes())
	useDefault(t, rec)
	var limitSawExchange bool
	limit := func(c *gin.Context) {
		limitSawExchange = FromContext(c.Request.Context()) != nil
		c.Next()
	}
	r := gin.New()
	r.Handle(http.MethodPost, "/responses", Around(limit), fakeAuth(testAPIKey()), func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		c.Data(http.StatusOK, "application/json", bytes.ToUpper(body))
	})
	w := postJSON(r, "/responses", `{"a":1}`)
	require.Equal(t, `{"A":1}`, w.Body.String())
	require.True(t, limitSawExchange)
	dir, m := onlyExchange(t, rec)
	require.Equal(t, `{"a":1}`, readFile(t, dir, m.Client.RequestFile))
	require.Equal(t, "client.resp.json", m.Client.ResponseFile)
}

func TestMiddlewarePassthroughWithoutDefaultRecorder(t *testing.T) {
	useDefault(t, nil)
	r := gin.New()
	r.POST("/v1/messages", GinMiddleware(), func(c *gin.Context) {
		require.Nil(t, FromContext(c.Request.Context()))
		c.String(http.StatusOK, "ok")
	})
	require.Equal(t, "ok", postJSON(r, "/v1/messages", `{}`).Body.String())
}

// errAfterBody 先吐出 data，随后以 err 结束（模拟上游读在终止事件之后因 context 取消而报错）。
type errAfterBody struct {
	r   *strings.Reader
	err error
}

func (b *errAfterBody) Read(p []byte) (int, error) {
	if b.r.Len() == 0 {
		return 0, b.err
	}
	return b.r.Read(p)
}

func (b *errAfterBody) Close() error { return nil }

// oh-my-pi 收到 response.completed 后立即断开：客户端 ctx 被取消、上游读报 context canceled，
// 但 C/D 都已包含终止事件，数据完整，不应标为 incomplete。
func TestMiddlewareClientDisconnectAfterTerminalEventIsComplete(t *testing.T) {
	for name, partial := range map[string]bool{"after terminal": false, "before terminal": true} {
		t.Run(name, func(t *testing.T) {
			rec := newTestRecorder(t, enabledScopes())
			useDefault(t, rec)
			sent := upstreamSSE
			if partial {
				sent = upstreamSSE[:strings.Index(upstreamSSE, "event: response.completed")]
			}
			fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
				h := http.Header{"Content-Type": {"text/event-stream"}}
				return &http.Response{StatusCode: 200, Header: h, Body: &errAfterBody{r: strings.NewReader(sent), err: context.Canceled}}, nil
			}}
			up := WrapHTTPUpstream(fake, rec)
			ctx, cancel := context.WithCancel(context.Background())
			handler := func(c *gin.Context) {
				_, _ = io.ReadAll(c.Request.Body)
				req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://api.example.com/v1/responses", strings.NewReader(upstreamBody))
				resp, err := up.Do(req, "", 7, 1)
				require.NoError(t, err)
				defer func() { _ = resp.Body.Close() }()
				c.Header("Content-Type", "text/event-stream")
				c.Status(http.StatusOK)
				_, copyErr := io.Copy(c.Writer, resp.Body)
				require.ErrorIs(t, copyErr, context.Canceled)
				cancel() // 客户端断开
			}
			engine := newGatewayEngine(rec, up, testAPIKey(), handler)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(clientBody)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(httptest.NewRecorder(), req)

			_, m := onlyExchange(t, rec)
			require.True(t, m.Client.Canceled, "the disconnect itself must still be recorded")
			require.Equal(t, "context canceled", m.Attempts[0].Error)
			if partial {
				require.Equal(t, StatusIncomplete, m.Status)
				require.Equal(t, OutcomeError, m.Attempts[0].Outcome)
				require.Nil(t, m.FinalAttempt)
				return
			}
			require.Equal(t, StatusComplete, m.Status)
			require.Equal(t, OutcomeClosedEarly, m.Attempts[0].Outcome)
			require.NotNil(t, m.FinalAttempt)
			require.Equal(t, "resp_1", m.ResponseID)
			require.Len(t, m.Notes, 2)
		})
	}
}

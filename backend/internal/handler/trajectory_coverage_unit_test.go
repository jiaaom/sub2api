//go:build unit

package handler

// trajectory 录制的端到端覆盖测试（fork 专有，见 openspec/changes/add-trajectory-recording tasks 1.9）：
// 真实 handler + service，上游替换为桩并套上 trajectory 装饰器，断言每条主要协议路径都
// 产生了 exchange、上游尝试被归属到 exchange（orphan=0），且 A/B/C/D 四份数据都落盘。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/trajectory"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	trajAnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_traj\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":11,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	trajResponsesSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_traj\",\"object\":\"response\",\"model\":\"gpt-5.1\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\",\"annotations\":[]}]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_traj\",\"object\":\"response\",\"model\":\"gpt-5.1\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":9,\"output_tokens\":2,\"total_tokens\":11,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n"

	trajGeminiSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":1,\"totalTokenCount\":6},\"modelVersion\":\"gemini-2.5-flash\",\"responseId\":\"gem_traj\"}\n\n"
)

// trajectoryStubUpstream 按上游 URL 返回对应协议的 SSE。
type trajectoryStubUpstream struct {
	service.HTTPUpstream
	mu    sync.Mutex
	paths []string
}

func (u *trajectoryStubUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	u.mu.Lock()
	u.paths = append(u.paths, req.URL.Path)
	u.mu.Unlock()
	path := strings.ToLower(req.URL.Path)
	body := trajAnthropicSSE
	switch {
	case strings.Contains(path, "generatecontent"):
		body = trajGeminiSSE
	case strings.Contains(path, "responses"):
		body = trajResponsesSSE
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (u *trajectoryStubUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, conc int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, conc)
}

func newTrajectoryCoverageRecorder(t *testing.T, groupIDs ...int64) *trajectory.Recorder {
	t.Helper()
	rec, err := trajectory.New(trajectory.Options{Root: t.TempDir()}, nil)
	require.NoError(t, err)
	scopes := []trajectory.Scope{{Type: trajectory.ScopeGlobal, Enabled: true}}
	for _, id := range groupIDs {
		scopes = append(scopes, trajectory.Scope{Type: trajectory.ScopeGroup, ID: id, Enabled: true})
	}
	rec.SetScopes(scopes)
	prev := trajectory.Default()
	trajectory.SetDefault(rec)
	t.Cleanup(func() {
		trajectory.SetDefault(prev)
		rec.Close()
	})
	return rec
}

// serveRecorded 按网关链的顺序执行：trajectory（链首）→ clientRequestID → 鉴权 → handler。
func serveRecorded(t *testing.T, apiKey *service.APIKey, method, route, path, body string, call gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.Handle(method, route,
		trajectory.GinMiddleware(),
		func(c *gin.Context) {
			ctx := context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, "traj-crid")
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		},
		func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyAPIKey), apiKey)
			concurrency := 0
			if apiKey.User != nil {
				concurrency = apiKey.User.Concurrency
			}
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: concurrency})
			ctx := context.WithValue(c.Request.Context(), ctxkey.UserID, apiKey.UserID)
			ctx = context.WithValue(ctx, ctxkey.Group, apiKey.Group)
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		},
		call,
	)
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type trajectoryCoverageResult struct {
	dir  string
	meta trajectory.Meta
}

func requireOneRecordedExchange(t *testing.T, rec *trajectory.Recorder, clientBody string) trajectoryCoverageResult {
	t.Helper()
	rec.Flush()
	dirs, err := filepath.Glob(filepath.Join(rec.Root(), "raw", "*", "u*", "*"))
	require.NoError(t, err)
	require.Len(t, dirs, 1, "expected exactly one recorded exchange")
	raw, err := os.ReadFile(filepath.Join(dirs[0], "meta.json"))
	require.NoError(t, err)
	var m trajectory.Meta
	require.NoError(t, json.Unmarshal(raw, &m))

	stats := rec.Stats()
	require.Zero(t, stats.OrphanCalls, "every upstream call must be attributed to the exchange")
	require.Zero(t, stats.LateAttempts)
	require.Equal(t, trajectory.StatusComplete, m.Status, string(raw))
	require.Equal(t, "traj-crid", m.Client.ClientRequestID)
	require.NotEmpty(t, m.Attempts, "upstream attempt (B/C) must be recorded")

	got, err := os.ReadFile(filepath.Join(dirs[0], m.Client.RequestFile))
	require.NoError(t, err)
	require.Equal(t, clientBody, string(got), "A must be the original client body")
	require.NotEmpty(t, m.Client.ResponseFile, "D must be recorded")

	final := m.Attempts[len(m.Attempts)-1]
	require.Contains(t, []string{trajectory.OutcomeSuccess, trajectory.OutcomeClosedEarly}, final.Outcome)
	require.NotEmpty(t, final.RequestFile)
	require.NotEmpty(t, final.ResponseFile)
	b, err := os.ReadFile(filepath.Join(dirs[0], final.RequestFile))
	require.NoError(t, err)
	require.NotEmpty(t, b, "B must be the body actually sent upstream")
	return trajectoryCoverageResult{dir: dirs[0], meta: m}
}

func newTrajectoryOpenAIFixture(t *testing.T) (*OpenAIGatewayHandler, *service.APIKey, *trajectoryStubUpstream, *trajectory.Recorder) {
	t.Helper()
	groupID := int64(3131)
	rec := newTrajectoryCoverageRecorder(t, groupID)
	stub := &trajectoryStubUpstream{}
	h := newOpenAIResponsesFailoverTestHandler(t, trajectory.WrapHTTPUpstream(stub, rec))
	apiKey := &service.APIKey{
		ID: 99, UserID: 100, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowMessagesDispatch: true},
		User:  &service.User{ID: 100},
	}
	return h, apiKey, stub, rec
}

func TestTrajectoryCoverage_OpenAIResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, apiKey, stub, rec := newTrajectoryOpenAIFixture(t)
	body := `{"model":"gpt-5.1","stream":true,"input":"hello"}`
	w := serveRecorded(t, apiKey, http.MethodPost, "/v1/responses", "/v1/responses", body, h.Responses)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotEmpty(t, stub.paths)

	r := requireOneRecordedExchange(t, rec, body)
	require.Equal(t, trajectory.ProtocolOpenAIResponses, r.meta.Client.Protocol)
	require.Equal(t, trajectory.ProtocolOpenAIResponses, r.meta.Attempts[0].UpstreamProtocol)
	require.Equal(t, "resp_traj", r.meta.ResponseID)
}

func TestTrajectoryCoverage_OpenAIChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, apiKey, _, rec := newTrajectoryOpenAIFixture(t)
	body := `{"model":"gpt-5.1","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	w := serveRecorded(t, apiKey, http.MethodPost, "/v1/chat/completions", "/v1/chat/completions", body, h.ChatCompletions)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	r := requireOneRecordedExchange(t, rec, body)
	require.Equal(t, trajectory.ProtocolOpenAIChat, r.meta.Client.Protocol)
	require.True(t, r.meta.Client.Stream)
}

func TestTrajectoryCoverage_AnthropicMessagesOnOpenAIGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, apiKey, _, rec := newTrajectoryOpenAIFixture(t)
	body := `{"model":"gpt-5.1","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`
	w := serveRecorded(t, apiKey, http.MethodPost, "/v1/messages", "/v1/messages", body, h.Messages)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	r := requireOneRecordedExchange(t, rec, body)
	require.Equal(t, trajectory.ProtocolAnthropicMessages, r.meta.Client.Protocol)
	require.Equal(t, trajectory.ProtocolOpenAIResponses, r.meta.Attempts[0].UpstreamProtocol,
		"cross-protocol conversion must be visible: client anthropic, upstream responses")
}

func newTrajectoryGeminiFixture(t *testing.T) (*GatewayHandler, *service.APIKey, *trajectory.Recorder) {
	t.Helper()
	groupID := int64(9300)
	accountID := int64(9301)
	rec := newTrajectoryCoverageRecorder(t, groupID)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformGemini, Status: service.StatusActive}
	account := &service.Account{
		ID: accountID, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key"}, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1,
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}
	h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
	t.Cleanup(cleanup)
	h.geminiCompatService = service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil,
		trajectory.WrapHTTPUpstream(&trajectoryStubUpstream{}, rec), nil, &config.Config{})
	apiKey := &service.APIKey{
		ID: 9302, UserID: 9303, GroupID: &groupID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9303, Concurrency: 10, Balance: 100},
	}
	return h, apiKey, rec
}

func TestTrajectoryCoverage_GeminiNative(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, apiKey, rec := newTrajectoryGeminiFixture(t)
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	w := serveRecorded(t, apiKey, http.MethodPost, "/v1beta/models/*modelAction",
		"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", body, h.GeminiV1BetaModels)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	r := requireOneRecordedExchange(t, rec, body)
	require.Equal(t, trajectory.ProtocolGemini, r.meta.Client.Protocol)
	require.Equal(t, trajectory.ProtocolGemini, r.meta.Attempts[0].UpstreamProtocol)
	require.Equal(t, "gemini-2.5-flash", r.meta.Model.Requested)
	require.NotContains(t, r.meta.Attempts[0].URL, "test-key", "upstream credentials must be redacted")
}

func TestTrajectoryCoverage_ChatCompletionsOnGeminiGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, apiKey, rec := newTrajectoryGeminiFixture(t)
	body := `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := serveRecorded(t, apiKey, http.MethodPost, "/v1/chat/completions", "/v1/chat/completions", body, h.ChatCompletions)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	r := requireOneRecordedExchange(t, rec, body)
	require.Equal(t, trajectory.ProtocolOpenAIChat, r.meta.Client.Protocol)
	require.Equal(t, trajectory.ProtocolGemini, r.meta.Attempts[0].UpstreamProtocol)
}

func TestTrajectoryCoverage_AnthropicMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9400)
	accountID := int64(9401)
	rec := newTrajectoryCoverageRecorder(t, groupID)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	account := &service.Account{
		ID: accountID, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-test-key-0000"}, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1,
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}
	h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
	t.Cleanup(cleanup)
	schedulerSnapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	h.gatewayService = service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple},
		schedulerSnapshot, nil, nil, nil, nil, nil,
		trajectory.WrapHTTPUpstream(&trajectoryStubUpstream{}, rec),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	apiKey := &service.APIKey{
		ID: 9402, UserID: 9403, GroupID: &groupID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9403, Concurrency: 10, Balance: 100},
	}
	body := `{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`
	w := serveRecorded(t, apiKey, http.MethodPost, "/v1/messages", "/v1/messages", body, h.Messages)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	r := requireOneRecordedExchange(t, rec, body)
	require.Equal(t, trajectory.ProtocolAnthropicMessages, r.meta.Client.Protocol)
	require.Equal(t, trajectory.ProtocolAnthropicMessages, r.meta.Attempts[0].UpstreamProtocol)
	require.Equal(t, "msg_traj", r.meta.ResponseID)
	require.NotContains(t, r.meta.Attempts[0].RequestHeaders.Get("X-Api-Key"), "test-key")
}

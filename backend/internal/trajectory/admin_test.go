package trajectory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// memScopeRepo 内存版 ScopeRepository + TargetLister。
type memScopeRepo struct {
	mu      sync.Mutex
	scopes  map[ScopeType]map[int64]bool
	targets ScopeTargets
}

func newMemScopeRepo(targets ScopeTargets) *memScopeRepo {
	return &memScopeRepo{scopes: map[ScopeType]map[int64]bool{}, targets: targets}
}

func (m *memScopeRepo) ListScopes(context.Context) ([]Scope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Scope
	for _, typ := range []ScopeType{ScopeGlobal, ScopeGroup, ScopeAPIKey} {
		for id, enabled := range m.scopes[typ] {
			out = append(out, Scope{Type: typ, ID: id, Enabled: enabled})
		}
	}
	return out, nil
}

func (m *memScopeRepo) UpsertScope(_ context.Context, sc Scope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scopes[sc.Type] == nil {
		m.scopes[sc.Type] = map[int64]bool{}
	}
	m.scopes[sc.Type][sc.ID] = sc.Enabled
	return nil
}

func (m *memScopeRepo) DeleteScope(_ context.Context, typ ScopeType, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.scopes[typ], id)
	return nil
}

func (m *memScopeRepo) ListTargets(context.Context) (*ScopeTargets, error) {
	return &m.targets, nil
}

func newAdminEngine(t *testing.T, repo ScopeRepository) (*Recorder, *gin.Engine) {
	t.Helper()
	rec, err := New(Options{Root: t.TempDir()}, repo)
	require.NoError(t, err)
	t.Cleanup(rec.Close)
	useDefault(t, rec)
	r := gin.New()
	RegisterAdminRoutes(r.Group("/api/v1/admin"))
	return rec, r
}

func adminCall[T any](t *testing.T, r http.Handler, method, path, body string) (int, T) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/admin/recording"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env struct {
		Code int `json:"code"`
		Data T   `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	return w.Code, env.Data
}

func TestAdminScopesToggleAndEffective(t *testing.T) {
	g5, g6 := int64(5), int64(6)
	repo := newMemScopeRepo(ScopeTargets{
		Groups: []GroupTarget{{ID: 5, Name: "coding", Platform: "openai"}, {ID: 6, Name: "claude", Platform: "anthropic"}},
		APIKeys: []APIKeyTarget{
			{ID: 34, Name: "pi", UserID: 12, GroupID: &g5},
			{ID: 35, Name: "cc", UserID: 12, GroupID: &g6},
			{ID: 36, Name: "loose", UserID: 12},
		},
	})
	rec, r := newAdminEngine(t, repo)

	code, view := adminCall[ScopesView](t, r, http.MethodGet, "/scopes", "")
	require.Equal(t, http.StatusOK, code)
	require.False(t, view.Global)
	require.Len(t, view.Groups, 2)
	require.Len(t, view.APIKeys, 3)
	require.Empty(t, view.Scopes)

	for _, body := range []string{
		`{"type":"global","id":0,"enabled":true}`,
		`{"type":"group","id":5,"enabled":true}`,
		`{"type":"api_key","id":35,"enabled":true}`,
		`{"type":"api_key","id":34,"enabled":false}`,
	} {
		code, view = adminCall[ScopesView](t, r, http.MethodPut, "/scopes", body)
		require.Equal(t, http.StatusOK, code, body)
	}
	require.True(t, view.Global)
	require.True(t, view.Groups[0].Enabled)
	require.False(t, view.Groups[1].Enabled)
	byID := map[int64]APIKeyScope{}
	for _, k := range view.APIKeys {
		byID[k.ID] = k
	}
	require.False(t, byID[34].Effective, "key override off beats group on")
	require.Equal(t, false, *byID[34].Override)
	require.True(t, byID[35].Effective, "key override on beats group off")
	require.Nil(t, byID[36].Override)
	require.False(t, byID[36].Effective)
	// 写入立即生效，不等周期刷新
	require.True(t, rec.decide(5, 99))
	require.False(t, rec.decide(5, 34))

	// null 删除 override，恢复继承
	_, view = adminCall[ScopesView](t, r, http.MethodPut, "/scopes", `{"type":"api_key","id":34,"enabled":null}`)
	for _, k := range view.APIKeys {
		if k.ID == 34 {
			require.Nil(t, k.Override)
			require.True(t, k.Effective)
		}
	}
	require.True(t, rec.decide(5, 34))

	// 指向已删除分组的配置单独列出
	_, view = adminCall[ScopesView](t, r, http.MethodPut, "/scopes", `{"type":"group","id":99,"enabled":true}`)
	require.Equal(t, []Scope{{Type: ScopeGroup, ID: 99, Enabled: true}}, stripTimes(view.Stale))

	code, _ = adminCall[ScopesView](t, r, http.MethodPut, "/scopes", `{"type":"group","id":0,"enabled":true}`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = adminCall[ScopesView](t, r, http.MethodPut, "/scopes", `not json`)
	require.Equal(t, http.StatusBadRequest, code)
}

func stripTimes(in []Scope) []Scope {
	out := make([]Scope, len(in))
	for i, sc := range in {
		out[i] = Scope{Type: sc.Type, ID: sc.ID, Enabled: sc.Enabled}
	}
	return out
}

func TestAdminStatusAndRecentExchanges(t *testing.T) {
	repo := newMemScopeRepo(ScopeTargets{})
	rec, r := newAdminEngine(t, repo)
	rec.SetScopes(enabledScopes())

	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		return textResponse(200, "text/event-stream", upstreamSSE), nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	engine := newGatewayEngine(rec, up, testAPIKey(), proxyHandler(up, upstreamBody, 1))
	for range 3 {
		require.Equal(t, http.StatusOK, postJSON(engine, "/v1/responses", clientBody).Code)
	}
	rec.Flush()

	code, st := adminCall[StatusView](t, r, http.MethodGet, "/status", "")
	require.Equal(t, http.StatusOK, code)
	require.True(t, st.Available)
	require.True(t, st.GlobalEnabled)
	require.Equal(t, int64(3), st.Exchanges)
	require.Equal(t, 1, st.Days)
	require.Positive(t, st.DiskBytes)
	require.Zero(t, st.SpoolPending)
	require.NotNil(t, st.LastExchange)

	_, list := adminCall[ExchangeList](t, r, http.MethodGet, "/exchanges?limit=2", "")
	require.Len(t, list.Items, 2)
	require.True(t, list.HasMore)
	require.False(t, list.Items[0].StartedAt.Before(list.Items[1].StartedAt), "newest first")
	require.Equal(t, "resp_1", list.Items[0].ResponseID)

	_, list = adminCall[ExchangeList](t, r, http.MethodGet, "/exchanges", "")
	require.Len(t, list.Items, 3)
	require.False(t, list.HasMore)
}

func TestAdminWithoutRecorder(t *testing.T) {
	useDefault(t, nil)
	r := gin.New()
	RegisterAdminRoutes(r.Group("/api/v1/admin"))
	code, st := adminCall[StatusView](t, r, http.MethodGet, "/status", "")
	require.Equal(t, http.StatusOK, code)
	require.False(t, st.Available)
	code, _ = adminCall[ScopesView](t, r, http.MethodGet, "/scopes", "")
	require.Equal(t, http.StatusServiceUnavailable, code)
}

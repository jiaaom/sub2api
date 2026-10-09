package routes

// trajectory 录制的路由覆盖测试（fork 专有，openspec add-trajectory-recording tasks 1.9）：
// 用真实的 RegisterGatewayRoutes 注册全部网关路由，对每一条 POST 路由发请求，断言
// 每条路由都产生了 exchange（即 trajectory middleware 位于该路由链上）、每个路由组都被覆盖、orphan=0。
// handler 依赖全部为零值，panic 由 Recovery 兜住——这里只验证“链上有录制”，真实转发路径的
// 上游归属由 internal/handler/trajectory_coverage_unit_test.go 覆盖。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/trajectory"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// trajectoryRouteGroup 按路径前缀归类网关路由组（与 gateway.go 中的 Group 一一对应，其余为 rootRoute 别名）。
func trajectoryRouteGroup(path string) string {
	for _, prefix := range []string{"/antigravity/v1beta/", "/antigravity/v1/", "/backend-api/codex/", "/v1beta/", "/v1/"} {
		if strings.HasPrefix(path, prefix) {
			return strings.TrimSuffix(prefix, "/")
		}
	}
	return "root"
}

func fillTrajectoryRoutePath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		switch {
		case p == "*modelAction":
			parts[i] = "gemini-2.5-flash:streamGenerateContent"
		case p == "*subpath":
			parts[i] = "compact"
		case strings.HasPrefix(p, ":"), strings.HasPrefix(p, "*"):
			parts[i] = "x1"
		}
	}
	return strings.Join(parts, "/")
}

func TestTrajectoryRecordingCoversEveryGatewayRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec, err := trajectory.New(trajectory.Options{Root: t.TempDir()}, nil)
	require.NoError(t, err)
	groupID := int64(77)
	rec.SetScopes([]trajectory.Scope{
		{Type: trajectory.ScopeGlobal, Enabled: true},
		{Type: trajectory.ScopeGroup, ID: groupID, Enabled: true},
	})
	prev := trajectory.Default()
	trajectory.SetDefault(rec)
	t.Cleanup(func() {
		trajectory.SetDefault(prev)
		rec.Close()
	})

	apiKey := &service.APIKey{ID: 1, UserID: 2, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformAnthropic}}
	router := gin.New()
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) { c.AbortWithStatus(http.StatusInternalServerError) }))
	// 在任何路由链之前放好 API key：即使某个组的鉴权 middleware 因零值依赖失败，录制也能完成判定。
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
		c.Next()
	})
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
			AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) { c.Next() }),
		nil, nil, nil, nil, nil,
		&config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1 << 20, TextMaxBodySize: 1 << 20}},
	)

	sent := map[string]string{} // 请求路径 → 路由组
	for _, route := range router.Routes() {
		if route.Method != http.MethodPost {
			continue
		}
		path := fillTrajectoryRoutePath(route.Path)
		if _, dup := sent[path]; dup {
			continue
		}
		sent[path] = trajectoryRouteGroup(route.Path)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"claude-sonnet-4-5","stream":false}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
	require.NotEmpty(t, sent)

	// GET（含 WebSocket upgrade）只挂句柄不录。
	for _, path := range []string{"/v1/models", "/v1/responses", "/backend-api/codex/responses", "/v1beta/models"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	rec.Flush()
	dirs, err := filepath.Glob(filepath.Join(rec.Root(), "raw", "*", "u*", "*"))
	require.NoError(t, err)
	recorded := map[string]int{}
	for _, dir := range dirs {
		raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		require.NoError(t, err)
		var m trajectory.Meta
		require.NoError(t, json.Unmarshal(raw, &m))
		require.Equal(t, http.MethodPost, m.Client.Method, "GET requests must not be recorded")
		recorded[m.Client.Path]++
	}

	perGroup := map[string][2]int{} // 组 → {POST 路由数, 已录制数}
	var missing []string
	for path, group := range sent {
		c := perGroup[group]
		c[0]++
		if recorded[path] == 1 {
			c[1]++
		} else {
			missing = append(missing, path)
		}
		perGroup[group] = c
	}
	groups := make([]string, 0, len(perGroup))
	for g := range perGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		t.Logf("route group %-20s POST routes=%3d recorded exchanges=%3d", g, perGroup[g][0], perGroup[g][1])
	}
	stats := rec.Stats()
	t.Logf("total POST routes=%d exchanges=%d orphan_calls=%d", len(sent), len(dirs), stats.OrphanCalls)

	sort.Strings(missing)
	require.Empty(t, missing, "every gateway POST route must be recorded exactly once")
	require.Len(t, dirs, len(sent))
	for _, g := range []string{"/v1", "/v1beta", "/backend-api/codex", "/antigravity/v1", "/antigravity/v1beta", "root"} {
		require.Positive(t, perGroup[g][1], "route group %s must produce exchanges", g)
	}
	require.Zero(t, stats.OrphanCalls)
}

package trajectory

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	testUserID  int64 = 12
	testKeyID   int64 = 34
	testGroupID int64 = 5
)

func enabledScopes() []Scope {
	return []Scope{{Type: ScopeGlobal, Enabled: true}, {Type: ScopeGroup, ID: testGroupID, Enabled: true}}
}

func newTestRecorder(t *testing.T, scopes []Scope) *Recorder {
	t.Helper()
	rec, err := New(Options{Root: t.TempDir()}, nil)
	require.NoError(t, err)
	rec.SetScopes(scopes)
	t.Cleanup(rec.Close)
	return rec
}

// useDefault 把 rec 设为进程默认录制器，测试结束后恢复。
func useDefault(t *testing.T, rec *Recorder) {
	t.Helper()
	prev := Default()
	SetDefault(rec)
	t.Cleanup(func() { SetDefault(prev) })
}

func testAPIKey() *service.APIKey {
	groupID := testGroupID
	return &service.APIKey{
		ID:      testKeyID,
		UserID:  testUserID,
		Name:    "pi-laptop",
		GroupID: &groupID,
		Group:   &service.Group{ID: testGroupID, Name: "coding"},
	}
}

// fakeAuth 模拟 apiKeyAuth：把 API key 放进 gin context。
func fakeAuth(key *service.APIKey) gin.HandlerFunc {
	return func(c *gin.Context) {
		if key != nil {
			c.Set(string(servermiddleware.ContextKeyAPIKey), key)
		}
		c.Next()
	}
}

// fakeUpstream 可编程的 service.HTTPUpstream。
type fakeUpstream struct {
	mu       sync.Mutex
	bodies   [][]byte
	respond  func(req *http.Request) (*http.Response, error)
	tlsCalls int
}

func (f *fakeUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()
	return f.respond(req)
}

func (f *fakeUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, conc int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	f.mu.Lock()
	f.tlsCalls++
	f.mu.Unlock()
	return f.Do(req, proxyURL, accountID, conc)
}

func textResponse(status int, contentType, body string) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", contentType)
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// rawExchanges 列出 raw/ 下全部 exchange 目录。
func rawExchanges(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, dirRaw, "*", "u*", "*"))
	require.NoError(t, err)
	return matches
}

func onlyExchange(t *testing.T, rec *Recorder) (string, Meta) {
	t.Helper()
	rec.Flush()
	dirs := rawExchanges(t, rec.Root())
	require.Len(t, dirs, 1, "expected exactly one recorded exchange")
	return dirs[0], readMeta(t, dirs[0])
}

func readMeta(t *testing.T, dir string) Meta {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fileMeta))
	require.NoError(t, err)
	var m Meta
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	require.NotEmpty(t, rel)
	raw, err := os.ReadFile(filepath.Join(dir, rel))
	require.NoError(t, err)
	return string(raw)
}

var errTest = errors.New("test failure")

func init() { gin.SetMode(gin.TestMode) }

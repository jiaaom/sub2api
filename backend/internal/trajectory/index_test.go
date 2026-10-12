package trajectory

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// memIndexStore 内存版 indexStore。
type memIndexStore struct {
	mu       sync.Mutex
	rows     []*ExchangeRow
	sessions map[string]*SessionRow
	state    map[string]string
}

func newMemIndexStore() *memIndexStore {
	return &memIndexStore{sessions: map[string]*SessionRow{}, state: map[string]string{}}
}

func (m *memIndexStore) insertExchange(_ context.Context, r *ExchangeRow) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.rows {
		if x.ExchangeID == r.ExchangeID {
			return false, nil
		}
	}
	cp := *r
	m.rows = append(m.rows, &cp)
	return true, nil
}

func (m *memIndexStore) upsertSession(_ context.Context, r *ExchangeRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[r.SessionID]
	if s == nil {
		s = &SessionRow{SessionID: r.SessionID, UserID: r.UserID, Hint: r.SessionHint, FirstAt: r.StartedAt, LastAt: r.StartedAt}
		m.sessions[r.SessionID] = s
	}
	s.Exchanges++
	if s.Title == "" {
		s.Title = r.Title
	}
	return nil
}

func (m *memIndexStore) findByResponseID(_ context.Context, userID int64, id string) (*ExchangeRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.UserID == userID && r.ResponseID == id {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *memIndexStore) findCandidates(_ context.Context, userID int64, before time.Time, exclude string, level int, hashes []string) ([]linkCandidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []linkCandidate
	for _, r := range m.rows {
		if r.UserID != userID || r.ExchangeID == exclude || r.StartedAt.After(before) {
			continue
		}
		if slices.Contains(hashes, r.Req[level]) || slices.Contains(hashes, r.Head[level]) {
			out = append(out, linkCandidate{ExchangeID: r.ExchangeID, SessionID: r.SessionID, StartedAt: r.StartedAt, Req: r.Req[level], Head: r.Head[level]})
		}
	}
	return out, nil
}

func (m *memIndexStore) countChildren(_ context.Context, parent, edge string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.rows {
		if r.ParentID != nil && *r.ParentID == parent && r.Edge == edge {
			n++
		}
	}
	return n, nil
}

func (m *memIndexStore) compactionCandidates(_ context.Context, userID int64, session string, since, before time.Time, limit int) ([]ExchangeRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ExchangeRow
	for i := len(m.rows) - 1; i >= 0 && len(out) < limit; i-- {
		r := m.rows[i]
		if r.UserID == userID && r.StartedAt.Before(before) && !r.StartedAt.Before(since) && r.RespTextLen >= compactionMinTextLen && (session == "" || r.SessionID == session) {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *memIndexStore) setKind(_ context.Context, id, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.ExchangeID == id {
			r.Kind = kind
		}
	}
	return nil
}

func (m *memIndexStore) existingIDs(_ context.Context, ids []string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, r := range m.rows {
		if slices.Contains(ids, r.ExchangeID) {
			out[r.ExchangeID] = true
		}
	}
	return out, nil
}

func (m *memIndexStore) truncate(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows, m.sessions = nil, map[string]*SessionRow{}
	return nil
}

func (m *memIndexStore) getState(_ context.Context, k string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[k], nil
}

func (m *memIndexStore) setState(_ context.Context, k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[k] = v
	return nil
}

// byTitle 按 exchange 发出时 tag 找行（tag 写在请求的 metadata 中，见 traj.send）。
func (m *memIndexStore) all() []ExchangeRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ExchangeRow, len(m.rows))
	for i, r := range m.rows {
		out[i] = *r
	}
	return out
}

// traj 通过真实的 middleware + 上游装饰器录制一串 Responses 请求，并用内存索引串联。
type traj struct {
	t      *testing.T
	rec    *Recorder
	store  *memIndexStore
	engine *gin.Engine
	ids    map[string]string // tag → exchange_id
	clock  time.Time
}

func newTraj(t *testing.T) *traj {
	tr := &traj{t: t, store: newMemIndexStore(), ids: map[string]string{}, clock: time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)}
	rec, err := New(Options{Root: t.TempDir(), Now: func() time.Time { return tr.clock }}, nil)
	require.NoError(t, err)
	rec.SetScopes(enabledScopes())
	rec.SetIndexer(newIndexer(tr.store, rec.Root()))
	t.Cleanup(rec.Close)
	useDefault(t, rec)
	tr.rec = rec

	var current string
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		return textResponse(200, "text/event-stream", current), nil
	}}
	up := WrapHTTPUpstream(fake, rec)
	handler := func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		current = c.GetHeader("X-Test-Response")
		req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://api.example.com/v1/responses", strings.NewReader(string(body)))
		resp, err := up.Do(req, "", 7, 1)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		c.Header("Content-Type", "text/event-stream")
		c.Status(resp.StatusCode)
		_, _ = io.Copy(c.Writer, resp.Body)
	}
	tr.engine = newGatewayEngine(rec, up, testAPIKey(), handler)
	return tr
}

// send 发一个请求；input 是 Responses input 数组的 JSON，output 是响应 output 数组的 JSON。
func (tr *traj) send(tag, hint, input, output string, extra ...string) {
	tr.t.Helper()
	tr.clock = tr.clock.Add(time.Minute)
	body := fmt.Sprintf(`{"model":"m","stream":true,"prompt_cache_key":%q,"instructions":"sys","tools":[{"type":"function","name":"read","parameters":{}}],"input":%s%s}`,
		hint, input, strings.Join(extra, ""))
	sse := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%s\",\"object\":\"response\",\"status\":\"completed\",\"output\":%s}}\n\n", tag, output)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Oh-My-Pi/18")
	req.Header.Set("X-Test-Response", sse)
	w := httptest.NewRecorder()
	tr.engine.ServeHTTP(w, req)
	require.Equal(tr.t, http.StatusOK, w.Code)
	tr.rec.Flush()
	rows := tr.store.all()
	tr.ids[tag] = rows[len(rows)-1].ExchangeID
}

func (tr *traj) row(tag string) ExchangeRow {
	tr.t.Helper()
	for _, r := range tr.store.all() {
		if r.ExchangeID == tr.ids[tag] {
			return r
		}
	}
	tr.t.Fatalf("no row for %s", tag)
	return ExchangeRow{}
}

func (tr *traj) requireEdge(tag, parentTag, edge, level string, prefix int) {
	tr.t.Helper()
	r := tr.row(tag)
	if parentTag == "" {
		require.Nil(tr.t, r.ParentID, tag)
	} else {
		require.NotNil(tr.t, r.ParentID, tag)
		require.Equal(tr.t, tr.ids[parentTag], *r.ParentID, tag)
	}
	require.Equal(tr.t, edge, r.Edge, tag)
	require.Equal(tr.t, level, r.MatchLevel, tag)
	require.Equal(tr.t, prefix, r.PrefixMessages, tag)
}

const (
	u1        = `{"role":"user","content":"hi there, please help"}`
	a1        = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}`
	u2        = `{"role":"user","content":"list folders"}`
	reasoning = `{"id":"rs_1","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"look at cwd"}]}`
	call      = `{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\": \".\"}"}`
	callBack  = `{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\".\"}"}`
	callOut   = `{"type":"function_call_output","call_id":"call_1","output":"src/"}`
	a2        = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There is src/."}]}`
	u2b       = `{"role":"user","content":"actually, show the README"}`
)

func arr(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

func TestIndexLinksOmpStyleConversation(t *testing.T) {
	tr := newTraj(t)
	tr.send("t1", "sess", arr(u1), arr(a1))
	tr.send("title", "aux", arr(`{"role":"user","content":"generate a title"}`), arr(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Greeting"}]}`))
	tr.send("t2", "sess", arr(u1, a1, u2), arr(reasoning, call))
	// 客户端去掉 reasoning、重排参数 JSON 后带回
	tr.send("t3", "sess", arr(u1, a1, u2, callBack, callOut), arr(a2))
	tr.send("t3-retry", "sess", arr(u1, a1, u2, callBack, callOut), arr(a2))
	// rewind：回到 t1 之后换个问题
	tr.send("t2-branch", "sess", arr(u1, a1, u2b), arr(a2))

	tr.requireEdge("t1", "", EdgeRoot, "", 0)
	tr.requireEdge("title", "", EdgeRoot, "", 0)
	tr.requireEdge("t2", "t1", EdgeContinue, "exact", 2)
	tr.requireEdge("t3", "t2", EdgeContinue, "no_thinking", 4)
	tr.requireEdge("t3-retry", "t3", EdgeRetry, "exact", 5)
	tr.requireEdge("t2-branch", "t1", EdgeBranch, "exact", 2)

	s := tr.row("t1").SessionID
	for _, tag := range []string{"t2", "t3", "t3-retry", "t2-branch"} {
		require.Equal(t, s, tr.row(tag).SessionID, tag)
	}
	require.NotEqual(t, s, tr.row("title").SessionID, "auxiliary call with its own hint gets its own session")
	require.Equal(t, "hi there, please help", tr.row("t1").Title)
	require.Equal(t, 5, tr.row("t3").ReqMessages)
	require.Equal(t, 1, tr.row("t3").Tools)
	require.Equal(t, 5, tr.store.sessions[s].Exchanges)
	require.Equal(t, "hi there, please help", tr.store.sessions[s].Title)
}

func TestIndexLinksCompactionByContent(t *testing.T) {
	tr := newTraj(t)
	summary := strings.Repeat("The user asked to refactor the parser; we split lexer and parser and fixed three tests. ", 4)
	tr.send("t1", "sess", arr(u1), arr(a1))
	tr.send("compact", "sess", arr(u1, a1, `{"role":"user","content":"Summarize the conversation so far."}`),
		arr(fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}`, summary)))
	tr.send("after", "sess", arr(fmt.Sprintf(`{"role":"user","content":%q}`, "This session continues from a previous conversation. Summary:\n\n"+summary+"\n\nPlease continue.")), arr(a2))

	tr.requireEdge("compact", "t1", EdgeContinue, "exact", 2)
	tr.requireEdge("after", "compact", EdgeCompaction, "summary", 0)
	require.Equal(t, KindCompactionRequest, tr.row("compact").Kind)
	require.Equal(t, KindMain, tr.row("after").Kind)
}

func TestIndexFollowsPreviousResponseID(t *testing.T) {
	tr := newTraj(t)
	tr.send("t1", "", arr(u1), arr(a1))
	tr.send("t2", "", arr(u2), arr(a2), `,"previous_response_id":"resp_t1"`)
	tr.requireEdge("t2", "t1", EdgePointer, "pointer", 0)
	require.Equal(t, tr.row("t1").SessionID, tr.row("t2").SessionID, "hint-less exchanges inherit the parent's session")
	// 指针展开后的链等价于完整历史：带完整历史的无状态请求能接在 t2 后面
	tr.send("t3", "", arr(u1, a1, u2, a2, `{"role":"user","content":"next"}`), arr(a1))
	tr.requireEdge("t3", "t2", EdgeContinue, "exact", 4)
}

func TestIndexCatchUpAndRebuildFromDisk(t *testing.T) {
	tr := newTraj(t)
	tr.send("t1", "sess", arr(u1), arr(a1))
	tr.send("t2", "sess", arr(u1, a1, u2), arr(reasoning, call))
	tr.send("t3", "sess", arr(u1, a1, u2, callBack, callOut), arr(a2))
	want := tr.store.all()

	// 新的空索引：启动时按开始时间补齐，串联结果一致
	fresh := newMemIndexStore()
	fresh.state[stateRulesVersion] = fmt.Sprint(IndexRulesVersion)
	ix := newIndexer(fresh, tr.rec.Root())
	ix.Start()
	ix.Flush()
	ix.Close()
	got := fresh.all()
	require.Len(t, got, 3)
	for i := range want {
		require.Equal(t, want[i].ExchangeID, got[i].ExchangeID)
		require.Equal(t, want[i].ParentID, got[i].ParentID)
		require.Equal(t, want[i].Edge, got[i].Edge)
		require.Equal(t, want[i].Head, got[i].Head)
	}

	// 规则版本不一致：清空后全量重建
	fresh.state[stateRulesVersion] = "0"
	_, _ = fresh.insertExchange(context.Background(), &ExchangeRow{ExchangeID: "stale"})
	ix = newIndexer(fresh, tr.rec.Root())
	ix.Start()
	ix.Flush()
	ix.Close()
	require.Len(t, fresh.all(), 3)
	require.Equal(t, fmt.Sprint(IndexRulesVersion), fresh.state[stateRulesVersion])

	// 手动重建
	ix = newIndexer(fresh, tr.rec.Root())
	ix.Start()
	require.True(t, ix.Rebuild())
	ix.Flush()
	ix.Close()
	require.Len(t, fresh.all(), 3)
}

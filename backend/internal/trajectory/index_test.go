package trajectory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
			out = append(out, linkCandidate{ExchangeID: r.ExchangeID, SessionID: r.SessionID, StartedAt: r.StartedAt, Req: r.Req[level], Head: r.Head[level], Kind: r.Kind})
		}
	}
	return out, nil
}

func (m *memIndexStore) countChildren(_ context.Context, parent, edge, exclude string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.rows {
		if r.ParentID != nil && *r.ParentID == parent && r.Edge == edge && r.ExchangeID != exclude {
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

func (m *memIndexStore) getExchange(_ context.Context, id string) (*ExchangeRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.ExchangeID == id {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *memIndexStore) startedBetween(_ context.Context, userID int64, after, until time.Time, exclude string) ([]ExchangeRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ExchangeRow
	for _, r := range m.rows {
		if r.UserID == userID && r.ExchangeID != exclude && r.StartedAt.After(after) && !r.StartedAt.After(until) {
			out = append(out, *r)
		}
	}
	slices.SortFunc(out, func(a, b ExchangeRow) int { return a.StartedAt.Compare(b.StartedAt) })
	return out, nil
}

func (m *memIndexStore) latestMainBefore(_ context.Context, session string, before time.Time, excludeRoot string) (*ExchangeRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *ExchangeRow
	for _, r := range m.rows {
		if r.SessionID == session && r.StartedAt.Before(before) && r.Kind == KindMain && r.RootHash != excludeRoot && r.RootHash != "" &&
			(best == nil || r.StartedAt.After(best.StartedAt)) {
			best = r
		}
	}
	if best == nil {
		return nil, nil
	}
	cp := *best
	return &cp, nil
}

func (m *memIndexStore) markSummaries(_ context.Context, session, root string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.SessionID == session && r.RootHash == root && r.Kind == KindMain {
			r.Kind = KindSummary
		}
	}
	return nil
}

func (m *memIndexStore) updateLink(_ context.Context, u *ExchangeRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.ExchangeID == u.ExchangeID {
			r.ParentID, r.Edge, r.MatchLevel, r.PrefixMessages = u.ParentID, u.Edge, u.MatchLevel, u.PrefixMessages
			r.Kind, r.ParentCallID = u.Kind, u.ParentCallID
		}
	}
	return nil
}

func (m *memIndexStore) spawnCandidates(_ context.Context, userID int64, since, before time.Time, exclude string, limit int) ([]ExchangeRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ExchangeRow
	for i := len(m.rows) - 1; i >= 0 && len(out) < limit; i-- {
		r := m.rows[i]
		if r.UserID == userID && r.ExchangeID != exclude && !r.StartedAt.Before(since) && r.StartedAt.Before(before) && len(r.SpawnCalls) > 0 {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *memIndexStore) sessionBusyBetween(_ context.Context, session string, after, before time.Time, parent string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		directNext := r.ParentID != nil && *r.ParentID == parent && (r.Edge == EdgeContinue || r.Edge == EdgePointer)
		if r.SessionID == session && r.ExchangeID != parent && r.StartedAt.After(after) && r.StartedAt.Before(before) &&
			r.Kind != KindSubagent && r.Kind != KindCountTokens && !directNext {
			return true, nil
		}
	}
	return false, nil
}

func (m *memIndexStore) setSessionParent(_ context.Context, session, parentSession, parentExchange, callID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[session]; s != nil {
		s.ParentSessionID, s.ParentExchangeID, s.ParentCallID = parentSession, parentExchange, callID
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
	// instructions 请求的 system（Responses instructions），默认 "sys"。
	instructions string
}

func newTraj(t *testing.T) *traj {
	tr := &traj{t: t, store: newMemIndexStore(), ids: map[string]string{}, clock: time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC), instructions: "sys"}
	rec, err := New(Options{Root: t.TempDir(), Now: func() time.Time { return tr.clock }}, nil)
	require.NoError(t, err)
	rec.SetScopes(enabledScopes())
	rec.SetIndexer(newIndexer(tr.store, rec.Root()))
	t.Cleanup(rec.Close)
	useDefault(t, rec)
	tr.rec = rec

	var current string
	fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
		if current == "502" { // 模拟上游失败
			return textResponse(502, "application/json", `{"error":{"message":"upstream unavailable"}}`), nil
		}
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
	body := fmt.Sprintf(`{"model":"m","stream":true,"prompt_cache_key":%q,"instructions":%q,"tools":[{"type":"function","name":"read","parameters":{}}],"input":%s%s}`,
		hint, tr.instructions, input, strings.Join(extra, ""))
	sse := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%s\",\"object\":\"response\",\"status\":\"completed\",\"output\":%s}}\n\n", tag, output)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Oh-My-Pi/18")
	if output == "502" {
		sse = "502"
	}
	req.Header.Set("X-Test-Response", sse)
	w := httptest.NewRecorder()
	tr.engine.ServeHTTP(w, req)
	if output != "502" {
		require.Equal(tr.t, http.StatusOK, w.Code)
	}
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

func TestIndexLinksDetachedSummaryCallIntoMainPath(t *testing.T) {
	tr := newTraj(t)
	summary := strings.Repeat("Goal: count FAILED lines in three logs; each had 193 of 2500. ", 5)
	tr.send("t1", "sess", arr(u1), arr(a1))
	tr.send("t2", "sess", arr(u1, a1, u2), arr(a2))
	// omp 式压缩：另起 system，把旧对话拍平成一条消息
	tr.instructions = "Summarize user-AI coding-assistant conversations."
	tr.send("summary", "sess", arr(`{"role":"user","content":"<conversation>hi there ... There is src/.</conversation>"}`),
		arr(fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}`, summary)))
	tr.send("side-summary", "sess", arr(`{"role":"user","content":"<conversation>only the tail</conversation>"}`),
		arr(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"A short recap that nobody uses."}]}`))
	tr.instructions = "sys"
	tr.send("after", "sess", arr(fmt.Sprintf(`{"role":"user","content":%q}`, "Prior model work available.\n<summary>\n"+summary+"\n</summary>"), `{"role":"user","content":"what were the counts?"}`), arr(a1))
	tr.send("after2", "sess", arr(fmt.Sprintf(`{"role":"user","content":%q}`, "Prior model work available.\n<summary>\n"+summary+"\n</summary>"), `{"role":"user","content":"what were the counts?"}`, a1, u2b), arr(a2))

	tr.requireEdge("after", "summary", EdgeCompaction, "summary", 0)
	tr.requireEdge("summary", "t2", EdgeSummarize, "session", 0)
	tr.requireEdge("after2", "after", EdgeContinue, "exact", 3)
	require.Equal(t, KindCompactionRequest, tr.row("summary").Kind)
	require.Equal(t, KindSummary, tr.row("side-summary").Kind, "other calls with the summarizer system are side summaries")
	require.Equal(t, KindMain, tr.row("t2").Kind)
}

func TestIndexRelinksChildIndexedBeforeLateParent(t *testing.T) {
	tr := newTraj(t)
	tr.send("t1", "sess", arr(u1), arr(a1))
	// 客户端在 t2 流式中途断开，随后发了新消息（不带 t2 的半截回答）
	tr.send("t2", "sess", arr(u1, a1, u2), arr(a2))
	tr.send("t3", "sess", arr(u1, a1, u2, u2b), arr(a1))
	want := map[string]ExchangeRow{}
	for _, tag := range []string{"t1", "t2", "t3"} {
		want[tag] = tr.row(tag)
	}
	require.Equal(t, EdgeBranch, want["t3"].Edge)

	// 重新索引，但 t2 比 t3 晚落定、晚入索引
	fresh := newMemIndexStore()
	ix := newIndexer(fresh, tr.rec.Root())
	ctx := context.Background()
	index := func(tag string, finishedAfter *time.Time) {
		dir := filepath.Join(tr.rec.Root(), filepath.FromSlash(want[tag].RawPath))
		meta, err := loadMetaFile(dir)
		require.NoError(t, err)
		if finishedAfter != nil {
			f := finishedAfter.Add(time.Second)
			meta.FinishedAt = &f
		}
		require.NoError(t, ix.indexOne(ctx, dir, meta))
	}
	index("t1", nil)
	index("t3", nil)
	before, _ := fresh.getExchange(ctx, want["t3"].ExchangeID)
	require.Equal(t, want["t1"].ExchangeID, *before.ParentID, "without t2 the best match is t1")
	t3Start := want["t3"].StartedAt
	index("t2", &t3Start)
	after, _ := fresh.getExchange(ctx, want["t3"].ExchangeID)
	require.Equal(t, want["t2"].ExchangeID, *after.ParentID, "relinked once the late parent is indexed")
	require.Equal(t, EdgeBranch, after.Edge)
	require.Equal(t, 3, after.PrefixMessages)
}

// 并行的另一个任务里有个长 exchange：它入索引时会重新串联本会话里已入库的 continue，结果不能变成 branch。
func TestIndexRelinkKeepsExistingContinue(t *testing.T) {
	tr := newTraj(t)
	tr.send("t1", "sess", arr(u1), arr(a1))
	tr.send("t2", "sess", arr(u1, a1, u2), arr(a2))
	tr.send("other", "other-task", arr(`{"role":"user","content":"long running elsewhere"}`), arr(a1))
	ctx := context.Background()
	other := tr.row("other")
	dir := filepath.Join(tr.rec.Root(), filepath.FromSlash(other.RawPath))
	meta, err := loadMetaFile(dir)
	require.NoError(t, err)
	late := rowFromMeta(meta, other.RawPath)
	late.StartedAt = tr.row("t1").StartedAt.Add(-time.Second)
	f := tr.row("t2").StartedAt.Add(time.Second)
	late.FinishedAt = &f
	ix := newIndexer(tr.store, tr.rec.Root())
	require.NoError(t, ix.relinkInFlight(ctx, late))
	tr.requireEdge("t2", "t1", EdgeContinue, "exact", 2)
}

// 上游连接超时：sub2api 回 502，omp 原样重发；成功之前的每次都是 error，重发判为 retry。
func TestIndexUpstreamFailuresAndResends(t *testing.T) {
	tr := newTraj(t)
	tr.send("t1", "sess", arr(u1), arr(a1))
	tr.send("fail1", "sess", arr(u1, a1, u2), "502")
	tr.send("fail2", "sess", arr(u1, a1, u2), "502")
	tr.send("ok", "sess", arr(u1, a1, u2), arr(a2))
	tr.send("next", "sess", arr(u1, a1, u2, a2, u2b), arr(a1))

	require.Equal(t, StatusError, tr.row("fail1").Status)
	require.Equal(t, StatusError, tr.row("fail2").Status)
	require.Equal(t, StatusComplete, tr.row("ok").Status)
	tr.requireEdge("fail1", "t1", EdgeContinue, "exact", 2)
	tr.requireEdge("fail2", "fail1", EdgeRetry, "exact", 3)
	tr.requireEdge("ok", "fail2", EdgeRetry, "exact", 3)
	tr.requireEdge("next", "ok", EdgeContinue, "exact", 4)

	dir := filepath.Join(tr.rec.Root(), filepath.FromSlash(tr.row("fail1").RawPath))
	m, err := loadMetaFile(dir)
	require.NoError(t, err)
	require.Equal(t, StatusError, m.Status, "finalize writes error when no attempt succeeded")
}

func TestIndexLinksSubagentToSpawningToolCall(t *testing.T) {
	tr := newTraj(t)
	task := "# Target\nFile: notes.md in the working directory.\n\n# Change\nCreate notes.md with exactly three bullet points about tail recursion."
	args, _ := json.Marshal(map[string]any{"i": "Delegating", "tasks": []map[string]string{{"name": "NotesWriter", "task": task}}})
	taskCall := fmt.Sprintf(`{"type":"function_call","call_id":"call_task","name":"task","arguments":%q}`, string(args))
	tr.send("main1", "main", arr(u1), arr(reasoning, taskCall))

	// 子 agent：自己的 system 与会话标识，第一条用户消息 = 前缀 + task 文本
	tr.instructions = "sub system with context"
	subFirst := fmt.Sprintf(`{"role":"user","content":[{"type":"input_text","text":"<system-reminder>cwd</system-reminder>"},{"type":"input_text","text":%q}]}`, "Complete assignment thoroughly:\n\n"+task)
	writeCall := `{"type":"function_call","call_id":"call_w","name":"write","arguments":"{\"path\":\"notes.md\"}"}`
	tr.send("sub1", "sub", arr(subFirst), arr(writeCall))
	tr.send("sub2", "sub", arr(subFirst, writeCall, `{"type":"function_call_output","call_id":"call_w","output":"ok"}`), arr(a2))

	// 主 agent 继续
	tr.instructions = "sys"
	tr.send("main2", "main", arr(u1, taskCall, `{"type":"function_call_output","call_id":"call_task","output":"Spawned agent NotesWriter"}`), arr(a1))

	// 反例：长文本只出现在 system 里，不算子 agent
	tr.instructions = "rules: " + task
	tr.send("other", "other", arr(`{"role":"user","content":"unrelated question that is long enough to be considered for matching, really it is"}`), arr(a1))

	tr.requireEdge("sub1", "main1", EdgeSubagent, "task_args", 0)
	require.Equal(t, "call_task", tr.row("sub1").ParentCallID)
	require.Equal(t, KindSubagent, tr.row("sub1").Kind)
	tr.requireEdge("sub2", "sub1", EdgeContinue, "exact", 2)
	require.Equal(t, KindSubagent, tr.row("sub2").Kind, "later subagent turns inherit the kind")
	tr.requireEdge("main2", "main1", EdgeContinue, "no_thinking", 2)
	require.Equal(t, KindMain, tr.row("main2").Kind)
	tr.requireEdge("other", "", EdgeRoot, "", 0)

	sub := tr.store.sessions[tr.row("sub1").SessionID]
	require.Equal(t, tr.row("main1").SessionID, sub.ParentSessionID)
	require.Equal(t, tr.ids["main1"], sub.ParentExchangeID)
	require.Equal(t, "call_task", sub.ParentCallID)
	require.True(t, strings.HasPrefix(tr.row("sub1").Title, "Complete assignment thoroughly: # Target"), tr.row("sub1").Title)
}

// 实录里的三类误判（2026-10-15 autoresearch）：工具参数（init_experiment 的 goal、log_experiment 的说明）
// 只是碰巧又出现在别的请求里，不是派出子 agent。
func TestIndexSubagentRejectsCoincidentalArgs(t *testing.T) {
	user := func(text string) string { return fmt.Sprintf(`{"role":"user","content":%q}`, text) }
	goal := "Minimize cost (METRIC cost, lower is better). Read AGENTS.md first. Then iterate on the kernel until the metric stops improving."
	note := "Unrolled the inner loop by four and pipelined the accumulator; latency dropped from 812us to 640us with no extra DSPs used."
	call := func(id, name, key, val string) string {
		args, _ := json.Marshal(map[string]string{key: val})
		return fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":%q,"arguments":%q}`, id, name, string(args))
	}
	initCall := call("call_init", "init_experiment", "goal", goal)
	logCall := call("call_log", "log_experiment", "description", note)
	out := func(id string) string {
		return fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":"ok"}`, id)
	}

	tr := newTraj(t)
	tr.send("run1", "run1", arr(user(goal)), arr(initCall))
	// 1) 父会话之后的压缩请求：对话转储里有 log_experiment 的参数，但不在开头
	tr.send("log", "run1", arr(user(goal), initCall, out("call_init")), arr(logCall))
	tr.instructions = "Summarize user–AI coding-assistant conversations in the exact specified structured format."
	dump := "<conversation>\n<system-notice>Background job bg_3 has completed. Resume your work using the result below.</system-notice>" +
		"<assistant>Baseline verified. Logging the experiment now.</assistant><tool_call>log_experiment " + note + "</tool_call></conversation>"
	tr.send("compact", "run1", arr(user(dump)), arr(a1))
	// 2) 前缀没接上的主线请求（有 assistant 消息），开头用户消息正是 goal
	tr.instructions = "sys v2"
	tr.send("rewritten", "run1", arr(user(goal), initCall, out("call_init"), logCall, out("call_log")), arr(a2))
	// 3) 父会话已经往下走之后，用同一段提示词另起的独立会话
	tr.instructions = "sys"
	tr.send("run2", "run2", arr(user(goal)), arr(a1))

	for _, name := range []string{"compact", "rewritten", "run2"} {
		require.NotEqual(t, EdgeSubagent, tr.row(name).Edge, name)
		require.NotEqual(t, KindSubagent, tr.row(name).Kind, name)
	}
	require.Empty(t, tr.store.sessions[tr.row("run2").SessionID].ParentSessionID)
}

// omp 的 task 工具立即返回：父 agent 的下一轮比子 agent 第一轮早几十毫秒发出（实录 2026-10-12 场景 G）。
// 父节点的直接接续不关闭执行窗口，再往后一轮才关。
func TestIndexSubagentAfterParentContinues(t *testing.T) {
	task := "# Target\nFile: notes.md in the working directory.\n\n# Change\nCreate notes.md with exactly three bullet points about tail recursion."
	args, _ := json.Marshal(map[string]any{"tasks": []map[string]string{{"name": "NotesWriter", "task": task}}})
	taskCall := fmt.Sprintf(`{"type":"function_call","call_id":"call_task","name":"task","arguments":%q}`, string(args))
	taskOut := `{"type":"function_call_output","call_id":"call_task","output":"Spawned agent NotesWriter"}`
	subFirst := fmt.Sprintf(`{"role":"user","content":%q}`, "Complete assignment thoroughly:\n\n"+task)

	tr := newTraj(t)
	tr.send("main1", "main", arr(u1), arr(taskCall))
	tr.send("main2", "main", arr(u1, taskCall, taskOut), arr(call))
	tr.instructions = "sub system"
	tr.send("sub1", "sub", arr(subFirst), arr(a1))
	tr.instructions = "sys"
	tr.send("main3", "main", arr(u1, taskCall, taskOut, call, `{"type":"function_call_output","call_id":"call_1","output":"x"}`), arr(a2))
	tr.instructions = "another sub system"
	tr.send("late", "late", arr(subFirst), arr(a1))

	tr.requireEdge("main2", "main1", EdgeContinue, "exact", 2)
	tr.requireEdge("sub1", "main1", EdgeSubagent, "task_args", 0)
	tr.requireEdge("late", "", EdgeRoot, "", 0)
}

// Claude Code 的子 agent 与父会话共用会话 id；并行派出的子 agent 互相不算“父会话往下走了”。
func TestIndexSubagentSameSessionParallel(t *testing.T) {
	user := func(text string) string { return fmt.Sprintf(`{"role":"user","content":%q}`, text) }
	p1 := "Search the repository for every place that parses the config file and list the functions with file paths."
	p2 := "Read the migration files under backend/migrations and summarize which tables each one creates or alters."
	args := func(p string) string {
		b, _ := json.Marshal(map[string]string{"description": "explore", "prompt": p})
		return string(b)
	}
	calls := fmt.Sprintf(`{"type":"function_call","call_id":"t1","name":"Task","arguments":%q}`, args(p1)) + "," +
		fmt.Sprintf(`{"type":"function_call","call_id":"t2","name":"Task","arguments":%q}`, args(p2))

	tr := newTraj(t)
	tr.send("main1", "cc", arr(u1), "["+calls+"]")
	tr.instructions = "subagent system"
	tr.send("subA", "cc", arr(user(p1)), arr(a1))
	tr.send("subB", "cc", arr(user(p2)), arr(a2))

	tr.requireEdge("subA", "main1", EdgeSubagent, "task_args", 0)
	tr.requireEdge("subB", "main1", EdgeSubagent, "task_args", 0)
	require.Equal(t, "t2", tr.row("subB").ParentCallID)
}

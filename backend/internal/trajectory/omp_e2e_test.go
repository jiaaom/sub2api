//go:build ompe2e

// 端到端校验：用 oh-my-pi 自己的会话日志（~/.omp/agent/sessions/*.jsonl 或 --session-dir）对照录制结果，
// 检查“沿主干逐轮拼出的对话”（与回放页相同的算法）是否与 omp 认为的对话一致。
//
//	OMP_SESSIONS=<dir> TRAJECTORY_ROOT=~/.local/share/sub2api/trajectories \
//	TRAJECTORY_PG=postgres://sub2api:sub2api@127.0.0.1:5432/sub2api?sslmode=disable \
//	GO111MODULE=on go test -tags ompe2e -count=1 -v -run TestOmpE2E ./internal/trajectory/
package trajectory

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/trajectory/ir"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// atom 对话中的一个可比较单元。
type atom struct {
	Kind string // user / assistant / tool_call / tool_result
	Text string
}

func (a atom) String() string {
	t := strings.Join(strings.Fields(a.Text), " ")
	if len(t) > 90 {
		t = t[:90] + "…"
	}
	return a.Kind + ": " + t
}

type ompEntry struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	ParentID string          `json:"parentId"`
	Message  json.RawMessage `json:"message"`
}

type ompMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"content"`
}

// ompConversation omp 会话：session id 与从最后一条消息回溯到根的消息路径。
func ompConversation(t *testing.T, path string) (string, []atom, int) {
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var sessionID, last string
	entries := map[string]ompEntry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	for sc.Scan() {
		var e ompEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Type == "session" {
			sessionID = e.ID
		}
		if e.ID != "" {
			entries[e.ID] = e
		}
		if e.Type == "message" || e.Type == "compaction" {
			last = e.ID
		}
	}
	var path_ []ompEntry
	for id := last; id != ""; id = entries[id].ParentID {
		if e := entries[id]; e.Type == "message" || e.Type == "compaction" {
			path_ = append(path_, e)
		}
	}
	var atoms []atom
	thinking := 0
	for i := len(path_) - 1; i >= 0; i-- {
		if path_[i].Type == "compaction" {
			atoms = append(atoms, atom{Kind: "compaction"})
			continue
		}
		var m ompMessage
		require.NoError(t, json.Unmarshal(path_[i].Message, &m))
		var texts []string
		for _, c := range m.Content {
			switch c.Type {
			case "text":
				texts = append(texts, c.Text)
			case "thinking":
				thinking++
			case "toolCall":
				atoms = append(atoms, atom{Kind: "tool_call", Text: c.Name + " " + canonArgs(c.Arguments)})
			}
		}
		switch m.Role {
		case "user":
			atoms = append([]atom(nil), append(atoms, atom{Kind: "user", Text: strings.Join(texts, "\n")})...)
		case "assistant":
			if len(texts) > 0 {
				// 文本在 tool_call 之前输出
				n := len(atoms)
				calls := 0
				for j := n - 1; j >= 0 && atoms[j].Kind == "tool_call"; j-- {
					calls++
				}
				txt := atom{Kind: "assistant", Text: strings.Join(texts, "\n")}
				atoms = append(atoms[:n-calls], append([]atom{txt}, atoms[n-calls:]...)...)
			}
		case "toolResult":
			atoms = append(atoms, atom{Kind: "tool_result", Text: strings.Join(texts, "\n")})
		}
	}
	return sessionID, atoms, thinking
}

func canonArgs(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return strings.TrimSpace(string(raw))
	}
	if s, ok := v.(string); ok { // 参数本身是 JSON 字符串
		var inner any
		if json.Unmarshal([]byte(s), &inner) == nil {
			v = inner
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// irAtoms 把 IR 消息转成 atom；返回 thinking 块数。
func irAtoms(msgs []ir.Message) ([]atom, int) {
	var out []atom
	thinking := 0
	for _, m := range msgs {
		var texts []string
		for _, b := range m.Blocks {
			switch b.Type {
			case ir.BlockText:
				if m.Role == ir.RoleUser && strings.HasPrefix(strings.TrimSpace(b.Text), "<system-reminder>") {
					continue // omp 注入的提醒不进它自己的会话日志
				}
				texts = append(texts, b.Text)
			case ir.BlockThinking, ir.BlockReasoningEncrypted, ir.BlockRedactedThinking:
				thinking++
			case ir.BlockToolCall:
				args := string(b.Arguments)
				if b.Arguments == nil {
					args = b.ArgumentsRaw
				}
				out = append(out, atom{Kind: "tool_call", Text: b.Name + " " + canonArgs(json.RawMessage(args))})
			case ir.BlockToolResult:
				var rt []string
				for _, c := range b.Content {
					rt = append(rt, c.Text)
				}
				out = append(out, atom{Kind: "tool_result", Text: strings.Join(rt, "\n")})
			}
		}
		if len(texts) > 0 {
			kind := "assistant"
			if m.Role == ir.RoleUser {
				kind = "user"
			}
			// 与 omp 一致：文本放在同一条消息的 tool_call 之前
			n := len(out)
			calls := 0
			for j := n - 1; j >= 0 && out[j].Kind == "tool_call" && m.Role == ir.RoleAssistant; j-- {
				calls++
			}
			txt := atom{Kind: kind, Text: strings.Join(texts, "\n")}
			out = append(out[:n-calls], append([]atom{txt}, out[n-calls:]...)...)
		}
	}
	return out, thinking
}

// trunk 与前端 trunkOf / displayFrom 相同的算法。
func trunk(rows []ExchangeRow) []ExchangeRow {
	byID := map[string]ExchangeRow{}
	var last *ExchangeRow
	for i := range rows {
		byID[rows[i].ExchangeID] = rows[i]
		if rows[i].Kind == KindMain {
			last = &rows[i]
		}
	}
	var out []ExchangeRow
	seen := map[string]bool{}
	for cur, ok := last, last != nil; ok && !seen[cur.ExchangeID]; {
		seen[cur.ExchangeID] = true
		out = append(out, *cur)
		r := *cur
		var next ExchangeRow
		ok = false
		if r.ParentID != nil {
			next, ok = byID[*r.ParentID]
		}
		for ok && r.Edge == EdgeRetry && !seen[next.ExchangeID] {
			seen[next.ExchangeID] = true
			r = next
			ok = false
			if r.ParentID != nil {
				next, ok = byID[*r.ParentID]
			}
		}
		if ok {
			n := next
			cur = &n
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func displayFromRow(row ExchangeRow, byID map[string]ExchangeRow, visible map[string]bool) int {
	r := row
	for r.Edge == EdgeRetry && r.ParentID != nil && !visible[*r.ParentID] {
		p, ok := byID[*r.ParentID]
		if !ok {
			break
		}
		r = p
	}
	if r.ParentID == nil || r.Edge == EdgeCompaction {
		return 0
	}
	return r.PrefixMessages
}

func TestOmpE2E(t *testing.T) {
	sessDir, root, dsn := os.Getenv("OMP_SESSIONS"), os.Getenv("TRAJECTORY_ROOT"), os.Getenv("TRAJECTORY_PG")
	if sessDir == "" || root == "" || dsn == "" {
		t.Skip("set OMP_SESSIONS, TRAJECTORY_ROOT and TRAJECTORY_PG")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	st := &pgIndexStore{db: db}
	files, _ := filepath.Glob(filepath.Join(sessDir, "*.jsonl"))
	sort.Strings(files)
	require.NotEmpty(t, files)
	for _, file := range files {
		ompID, want, ompThinking := ompConversation(t, file)
		t.Run(filepath.Base(file), func(t *testing.T) {
			var sid string
			err := db.QueryRow(`SELECT session_id FROM trajectory_sessions WHERE hint = $1`, ompID).Scan(&sid)
			require.NoError(t, err, "no recorded session for omp session %s", ompID)
			sess, rows, err := st.getSession(context.Background(), sid)
			require.NoError(t, err)
			tr := trunk(rows)
			byID := map[string]ExchangeRow{}
			for _, r := range rows {
				byID[r.ExchangeID] = r
			}
			visible := map[string]bool{}
			for _, r := range tr {
				visible[r.ExchangeID] = true
			}
			var got []atom
			ourThinking := 0
			edges := map[string]int{}
			for _, r := range rows {
				edges[r.Edge+"/"+r.MatchLevel+"/"+r.Status]++
			}
			// 规则（与 omp 日志的语义对齐）：
			//   - 压缩请求只记一个 compaction 标记；
			//   - compaction 新根的第一条消息是摘要载体，之后保留的旧消息与已出现的内容重复，均跳过；
			//   - 某轮的响应只有在下一轮把它带进历史（continue / pointer / summarize）时才算对话的一部分，
			//     中途中断、被下一轮丢弃的半截回答（branch）不算。
			seen := map[atom]bool{}
			for i, r := range tr {
				if r.Kind == KindCompactionRequest {
					got = append(got, atom{Kind: "compaction"})
					continue
				}
				dir := filepath.Join(root, filepath.FromSlash(r.RawPath))
				m, err := loadMetaFile(dir)
				require.NoError(t, err)
				p := parseExchange(dir, m)
				require.NoError(t, p.reqErr)
				msgs := p.req.Messages[displayFromRow(r, byID, visible):]
				if r.Edge == EdgeCompaction && len(msgs) > 0 {
					msgs = msgs[1:]
				}
				a, _ := irAtoms(msgs)
				for _, x := range a {
					if r.Edge == EdgeCompaction && seen[x] {
						continue
					}
					got = append(got, x)
					seen[x] = true
				}
				carried := i == len(tr)-1
				if !carried {
					switch tr[i+1].Edge {
					case EdgeContinue, EdgePointer, EdgeSummarize:
						carried = true
					}
				}
				if p.resp != nil && carried {
					a, th := irAtoms(p.resp.Messages)
					for _, x := range a {
						got = append(got, x)
						seen[x] = true
					}
					ourThinking += th
				}
			}
			t.Logf("omp %s → %s %q: %d exchanges, trunk %d, edges %v, thinking omp=%d recorded=%d",
				ompID, sid, sess.Title, len(rows), len(tr), edges, ompThinking, ourThinking)
			// 录制比 omp 日志更忠实的几处（模型确实看到了，但 omp 不以消息形式写进会话日志）：
			//   - 中断后 omp 注入一条 “You were saying this but I interrupted you: …” 用户消息；
			//   - 后台任务完成时注入 “<system-notice> Background job … has completed …” 用户消息；
			//   - 无事发生的工具结果在日志里写成 “[Uneventful result elided]”，实际发给模型的是全文。
			injected := 0
			filtered := got[:0:0]
			for _, g := range got {
				if g.Kind == "user" && (strings.HasPrefix(strings.TrimSpace(g.Text), "You were saying this but I interrupted you") ||
					strings.HasPrefix(strings.TrimSpace(g.Text), "<system-notice>")) {
					injected++
					continue
				}
				filtered = append(filtered, g)
			}
			got = filtered
			elided := 0
			diffs := 0
			for i := 0; i < max(len(want), len(got)); i++ {
				var w, g atom
				if i < len(want) {
					w = want[i]
				}
				if i < len(got) {
					g = got[i]
				}
				same := w.Kind == g.Kind && (strings.TrimSpace(w.Text) == strings.TrimSpace(g.Text) ||
					(w.Kind == "user" && strings.Contains(g.Text, strings.TrimSpace(w.Text))))
				if !same && w.Kind == "tool_result" && g.Kind == "tool_result" && strings.TrimSpace(w.Text) == "[Uneventful result elided]" {
					same = true
					elided++
				}
				if !same {
					diffs++
					if diffs <= 8 {
						t.Errorf("atom %d differs:\n  omp:      %v\n  recorded: %v", i, w, g)
					}
				}
			}
			require.Zero(t, diffs, "reconstructed conversation (%d atoms) differs from omp (%d atoms)", len(got), len(want))
			fmt.Printf("OK %s: %d atoms match (omp-elided tool results: %d, omp-injected interrupt notes: %d)\n", ompID, len(want), elided, injected)
		})
	}
}

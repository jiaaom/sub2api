package ir

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Level 规范化等级（design.md §6.4）。客户端把上一轮响应带回历史时常有改动，
// 所以每个 exchange 都按三个等级各算一条哈希链，串联时依次尝试。
type Level int

const (
	// LevelExact 只忽略不影响语义的差异：cache_control、key 顺序、参数 JSON 格式、首尾空白、签名和密文。
	LevelExact Level = iota
	// LevelNoThinking 在 Exact 基础上去掉 thinking / redacted / encrypted 块（客户端常不回传 CoT）。
	// 去掉后为空的消息（例如 Responses 单独的 reasoning item）整条跳过。
	LevelNoThinking
	// LevelLoose 只比较角色、文本和 tool_call 的 (id, name)。
	LevelLoose
)

// Levels 串联时的尝试顺序。
var Levels = []Level{LevelExact, LevelNoThinking, LevelLoose}

func (l Level) String() string {
	switch l {
	case LevelExact:
		return "exact"
	case LevelNoThinking:
		return "no_thinking"
	case LevelLoose:
		return "loose"
	}
	return "unknown"
}

// Chain 一条哈希链。Hashes[0] 是 h0（system + tools），Hashes[i] 对应 MsgIndex[i-1] 号消息。
// 被规范化跳过的消息不占位置，所以用 MsgIndex 映射回原消息下标。
type Chain struct {
	Hashes   []string `json:"hashes"`
	MsgIndex []int    `json:"msg_index"`
}

// Last 链尾哈希。
func (c Chain) Last() string { return c.Hashes[len(c.Hashes)-1] }

// Root h0 = H(canon(system) ‖ canon(tools))。与模型无关：中途换模型仍视为同一条轨迹。
func Root(req *Request) string {
	tools, _ := json.Marshal(canonTools(req.Tools))
	buf := append([]byte("h0\x00"), canonBlocks(req.System, LevelExact)...)
	buf = append(append(buf, 0), tools...)
	return hashHex(buf)
}

// RequestChain 从 h0 开始的请求链。
func RequestChain(req *Request, level Level) Chain {
	return Extend(Chain{Hashes: []string{Root(req)}}, req.Messages, 0, level)
}

// Extend 在 base 之后追加 msgs 的哈希；offset 是 msgs[0] 在完整消息序列中的下标。
func Extend(base Chain, msgs []Message, offset int, level Level) Chain {
	out := Chain{
		Hashes:   append(make([]string, 0, len(base.Hashes)+len(msgs)), base.Hashes...),
		MsgIndex: append(make([]int, 0, len(base.MsgIndex)+len(msgs)), base.MsgIndex...),
	}
	prev := out.Last()
	for i, m := range msgs {
		c, ok := CanonMessage(m, level)
		if !ok {
			continue
		}
		prev = hashHex([]byte(prev + "\x00" + string(c)))
		out.Hashes = append(out.Hashes, prev)
		out.MsgIndex = append(out.MsgIndex, offset+i)
	}
	return out
}

// HashString 128 位 sha256 前缀（32 位十六进制），用于派生稳定 id。
func HashString(s string) string { return hashHex([]byte(s)) }

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// CanonMessage 规范化一条消息。返回 false 表示该消息在此等级下为空、应跳过。
func CanonMessage(m Message, level Level) ([]byte, bool) {
	blocks := canonBlockList(m.Blocks, level)
	if len(blocks) == 0 {
		return nil, false
	}
	b, _ := json.Marshal(map[string]any{"r": canonRole(m.Role), "b": blocks})
	return b, true
}

func canonRole(role string) string {
	switch role {
	case "model":
		return RoleAssistant
	case "function":
		return RoleTool
	}
	return role
}

func canonBlocks(blocks []Block, level Level) []byte {
	b, _ := json.Marshal(canonBlockList(blocks, level))
	return b
}

// canonBlockList 规范化块列表：相邻的同类文本块先原样拼接再规范化
// （流式分块与客户端回传的粒度可能不同），空文本丢弃。
func canonBlockList(blocks []Block, level Level) []map[string]any {
	out := []map[string]any{}
	for _, b := range mergeAdjacentText(blocks) {
		if c := canonBlock(b, level); c != nil {
			out = append(out, c)
		}
	}
	return out
}

func mergeAdjacentText(blocks []Block) []Block {
	out := make([]Block, 0, len(blocks))
	for _, b := range blocks {
		if n := len(out); n > 0 && (b.Type == BlockText || b.Type == BlockThinking) && out[n-1].Type == b.Type {
			out[n-1].Text += b.Text
			continue
		}
		out = append(out, b)
	}
	return out
}

func canonBlock(b Block, level Level) map[string]any {
	switch b.Type {
	case BlockText:
		t := strings.TrimSpace(b.Text)
		if t == "" {
			return nil
		}
		return map[string]any{"t": BlockText, "x": t}
	case BlockThinking, BlockRedactedThinking, BlockReasoningEncrypted:
		if level != LevelExact {
			return nil
		}
		if b.Type == BlockThinking {
			return map[string]any{"t": BlockThinking, "x": strings.TrimSpace(b.Text)}
		}
		return map[string]any{"t": b.Type} // 密文不参与哈希
	case BlockToolCall:
		c := map[string]any{"t": BlockToolCall, "id": b.ID, "n": b.Name}
		if level != LevelLoose {
			c["a"] = canonArgs(b)
		}
		return c
	case BlockToolResult:
		if level == LevelLoose {
			return map[string]any{"t": BlockToolResult, "id": b.CallID}
		}
		return map[string]any{"t": BlockToolResult, "id": b.CallID, "e": b.IsError, "c": canonBlockList(b.Content, level)}
	case BlockImage:
		return map[string]any{"t": BlockImage, "o": b.Object}
	default:
		if level == LevelLoose {
			return map[string]any{"t": BlockUnknown, "k": b.Kind}
		}
		return map[string]any{"t": BlockUnknown, "k": b.Kind, "r": canonJSON(b.Raw)}
	}
}

func canonArgs(b Block) any {
	if b.Arguments != nil {
		return canonJSON(b.Arguments)
	}
	return strings.TrimSpace(b.ArgumentsRaw)
}

// canonJSON 解析后重新序列化（map 的 key 会排序），去掉 cache_control。
func canonJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	return stripCacheControl(v)
}

func stripCacheControl(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "cache_control")
		for k, e := range x {
			x[k] = stripCacheControl(e)
		}
	case []any:
		for i, e := range x {
			x[i] = stripCacheControl(e)
		}
	}
	return v
}

func canonTools(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if t.Raw != nil {
			out = append(out, map[string]any{"raw": canonJSON(t.Raw)})
			continue
		}
		out = append(out, map[string]any{"n": t.Name, "d": strings.TrimSpace(t.Description), "s": canonJSON(t.Schema)})
	}
	return out
}

package ir

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func sse(events ...string) []byte {
	flat := strings.NewReplacer("\n", "", "\t", "")
	out := ""
	for _, e := range events {
		out += "data: " + flat.Replace(e) + "\n\n"
	}
	return []byte(out)
}

// 结构取自 oh-my-pi + DeepSeek 的真实录制（内容已替换）。
const responsesReq1 = `{"model":"deepseek-v4-flash","stream":true,"store":false,"prompt_cache_key":"s1",
 "instructions":"You are a coding agent.","reasoning":{"effort":"high"},
 "tools":[{"type":"function","name":"read","description":"Read a path","parameters":{"type":"object","properties":{"path":{"type":"string"}}}},{"type":"web_search"}],
 "input":[
  {"role":"user","content":[{"type":"input_text","text":"<system-reminder>cwd</system-reminder>"},{"type":"input_text","text":"hi"}]},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello! What can I do?","annotations":[]}]},
  {"role":"user","content":[{"type":"input_text","text":"list folders"}]}
 ]}`

var responsesResp1 = sse(
	`{"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"deepseek-v4-flash"}}`,
	`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_0","type":"reasoning","summary":[],"content":[]}}`,
	`{"type":"response.reasoning_text.delta","output_index":0,"delta":"Let me look "}`,
	`{"type":"response.reasoning_text.delta","output_index":0,"delta":"at the workspace."}`,
	`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_0","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"Let me look at the workspace."}]}}`,
	`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":""}}`,
	`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\": \".\"}"}`,
	`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\": \".\"}"}}`,
	`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"deepseek-v4-flash",
	  "output":[{"id":"rs_0","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"Let me look at the workspace."}]},
	            {"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\": \".\"}"}],
	  "usage":{"input_tokens":18398,"output_tokens":63,"output_tokens_details":{"reasoning_tokens":8}}}}`,
)

// 下一轮：oh-my-pi 去掉了 reasoning item，参数 JSON 也被重新格式化。
var responsesReq2 = strings.Replace(responsesReq1, `{"role":"user","content":[{"type":"input_text","text":"list folders"}]}`,
	`{"role":"user","content":[{"type":"input_text","text":"list folders"}]},
  {"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\".\"}"},
  {"type":"function_call_output","call_id":"call_1","output":"src/\ndocs/"}`, 1)

func TestResponsesAdapter(t *testing.T) {
	req, err := ParseRequest(ProtocolOpenAIResponses, []byte(responsesReq1))
	require.NoError(t, err)
	require.Equal(t, "deepseek-v4-flash", req.Model)
	require.True(t, req.Stream)
	require.Equal(t, []Block{{Type: BlockText, Text: "You are a coding agent."}}, req.System)
	require.Len(t, req.Tools, 2)
	require.Equal(t, "read", req.Tools[0].Name)
	require.Equal(t, "web_search", req.Tools[1].Type)
	require.NotNil(t, req.Tools[1].Raw)
	require.Len(t, req.Messages, 3)
	require.Equal(t, RoleUser, req.Messages[0].Role)
	require.Len(t, req.Messages[0].Blocks, 2)
	require.Equal(t, RoleAssistant, req.Messages[1].Role)
	require.Contains(t, req.Params, "reasoning")
	require.Contains(t, req.Params, "prompt_cache_key")

	resp, err := ParseResponse(ProtocolOpenAIResponses, responsesResp1, true)
	require.NoError(t, err)
	require.True(t, resp.Complete)
	require.Equal(t, "resp_1", resp.ID)
	require.Equal(t, "completed", resp.StopReason)
	require.Equal(t, &Usage{Input: 18398, Output: 63, Reasoning: 8}, resp.Usage)
	require.Len(t, resp.Messages, 2)
	require.Equal(t, "reasoning", resp.Messages[0].ItemType)
	require.Equal(t, BlockThinking, resp.Messages[0].Blocks[0].Type)
	require.Equal(t, "Let me look at the workspace.", resp.Messages[0].Blocks[0].Text)
	call := resp.Messages[1].Blocks[0]
	require.Equal(t, BlockToolCall, call.Type)
	require.Equal(t, "call_1", call.ID)
	require.JSONEq(t, `{"path":"."}`, string(call.Arguments))

	req2, err := ParseRequest(ProtocolOpenAIResponses, []byte(responsesReq2))
	require.NoError(t, err)
	require.Equal(t, RoleTool, req2.Messages[4].Role)
	require.Equal(t, "call_1", req2.Messages[4].Blocks[0].CallID)
	require.Equal(t, "src/\ndocs/", req2.Messages[4].Blocks[0].Content[0].Text)
}

func TestResponsesStreamWithoutCompletedFallsBackToItemsAndDeltas(t *testing.T) {
	cut := responsesResp1[:strings.Index(string(responsesResp1), `data: {"type":"response.output_item.done","output_index":1`)]
	resp, err := ParseResponse(ProtocolOpenAIResponses, cut, true)
	require.NoError(t, err)
	require.False(t, resp.Complete)
	require.Len(t, resp.Messages, 2)
	require.Equal(t, "Let me look at the workspace.", resp.Messages[0].Blocks[0].Text)
	require.JSONEq(t, `{"path":"."}`, string(resp.Messages[1].Blocks[0].Arguments), "arguments rebuilt from deltas")
}

func TestHashChainLinksOmpTurnsAtNoThinkingLevel(t *testing.T) {
	req1, _ := ParseRequest(ProtocolOpenAIResponses, []byte(responsesReq1))
	resp1, _ := ParseResponse(ProtocolOpenAIResponses, responsesResp1, true)
	req2, _ := ParseRequest(ProtocolOpenAIResponses, []byte(responsesReq2))

	for _, tc := range []struct {
		level Level
		match bool
	}{{LevelExact, false}, {LevelNoThinking, true}, {LevelLoose, true}} {
		head := Extend(RequestChain(req1, tc.level), resp1.Messages, len(req1.Messages), tc.level)
		next := RequestChain(req2, tc.level)
		pos := indexOf(next.Hashes, head.Last())
		if !tc.match {
			require.Equal(t, -1, pos, tc.level.String())
			continue
		}
		require.Equal(t, len(head.Hashes)-1, pos, tc.level.String())
		// 匹配位置之后的第一条新消息是 function_call_output（下标 4）
		require.Equal(t, 4, next.MsgIndex[pos])
	}
	// 请求前缀在所有等级下都一致
	for _, l := range Levels {
		r1, r2 := RequestChain(req1, l), RequestChain(req2, l)
		require.Equal(t, r1.Hashes, r2.Hashes[:len(r1.Hashes)], l.String())
	}
}

func indexOf(hs []string, h string) int {
	for i, x := range hs {
		if x == h {
			return i
		}
	}
	return -1
}

const anthropicReq = `{"model":"claude-opus-5-5","stream":true,"max_tokens":32000,
 "system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],
 "tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
 "metadata":{"user_id":"user_x_session_abc"},
 "messages":[
  {"role":"user","content":"fix the bug"},
  {"role":"assistant","content":[{"type":"thinking","thinking":"I should run tests.","signature":"sig1"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]},
  {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"FAIL"}],"is_error":true,"cache_control":{"type":"ephemeral"}}]},
  {"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},{"type":"document","source":{"type":"text","data":"x"}}]}
 ]}`

var anthropicResp = sse(
	`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"claude-opus-5-5","usage":{"input_tokens":12,"cache_read_input_tokens":3000,"cache_creation_input_tokens":40,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The test "}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"fails."}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig2"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Fixing."}}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"Bash","input":{}}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"make\"}"}}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":321}}`,
	`{"type":"message_stop"}`,
)

func TestAnthropicAdapter(t *testing.T) {
	req, err := ParseRequest(ProtocolAnthropicMessages, []byte(anthropicReq))
	require.NoError(t, err)
	require.Equal(t, "You are Claude Code.", req.System[0].Text)
	require.Equal(t, "Bash", req.Tools[0].Name)
	require.Len(t, req.Messages, 4)
	require.Equal(t, []Block{{Type: BlockText, Text: "fix the bug"}}, req.Messages[0].Blocks)
	require.Equal(t, BlockThinking, req.Messages[1].Blocks[0].Type)
	require.Equal(t, "sig1", req.Messages[1].Blocks[0].Signature)
	require.JSONEq(t, `{"command":"go test ./..."}`, string(req.Messages[1].Blocks[1].Arguments))
	tr := req.Messages[2].Blocks[0]
	require.Equal(t, BlockToolResult, tr.Type)
	require.True(t, tr.IsError)
	require.Equal(t, "FAIL", tr.Content[0].Text)
	img := req.Messages[3].Blocks[0]
	require.Equal(t, BlockImage, img.Type)
	require.True(t, strings.HasPrefix(img.Object, "sha256:"))
	require.Equal(t, 8, img.Bytes)
	require.Equal(t, BlockUnknown, req.Messages[3].Blocks[1].Type)
	require.Equal(t, "document", req.Messages[3].Blocks[1].Kind)
	require.Contains(t, req.Params, "metadata")

	resp, err := ParseResponse(ProtocolAnthropicMessages, anthropicResp, true)
	require.NoError(t, err)
	require.True(t, resp.Complete)
	require.Equal(t, "tool_use", resp.StopReason)
	require.Equal(t, &Usage{Input: 12, Output: 321, CacheRead: 3000, CacheWrite: 40}, resp.Usage)
	require.Len(t, resp.Messages, 1)
	b := resp.Messages[0].Blocks
	require.Len(t, b, 3)
	require.Equal(t, Block{Type: BlockThinking, Text: "The test fails.", Signature: "sig2"}, b[0])
	require.Equal(t, "Fixing.", b[1].Text)
	require.Equal(t, "toolu_2", b[2].ID)
	require.JSONEq(t, `{"command":"make"}`, string(b[2].Arguments))

	nonStream := `{"id":"msg_9","type":"message","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`
	resp, err = ParseResponse(ProtocolAnthropicMessages, []byte(nonStream), false)
	require.NoError(t, err)
	require.True(t, resp.Complete)
	require.Equal(t, "ok", resp.Messages[0].Blocks[0].Text)
}

func TestCanonIgnoresCacheControlKeyOrderAndArgumentFormatting(t *testing.T) {
	a := Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolCall, ID: "1", Name: "f", Arguments: json.RawMessage(`{"a":1,"b":{"c":2}}`)}}}
	b := Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolCall, ID: "1", Name: "f", Arguments: json.RawMessage(`{ "b": {"c": 2}, "a": 1 }`)}}}
	ca, _ := CanonMessage(a, LevelExact)
	cb, _ := CanonMessage(b, LevelExact)
	require.Equal(t, string(ca), string(cb))

	// cache_control 从 system 移到别处不影响 h0
	r1, _ := ParseRequest(ProtocolAnthropicMessages, []byte(anthropicReq))
	r2, _ := ParseRequest(ProtocolAnthropicMessages, []byte(strings.Replace(anthropicReq, `,"cache_control":{"type":"ephemeral"}}],`, `}],`, 1)))
	require.Equal(t, Root(r1), Root(r2))
	require.Equal(t, RequestChain(r1, LevelExact).Hashes, RequestChain(r2, LevelExact).Hashes)

	// 文本分块粒度不同（流式 vs 回传）不影响哈希
	split := Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: "Hello"}, {Type: BlockText, Text: " world"}}}
	whole := Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: "Hello world\n"}}}
	cs, _ := CanonMessage(split, LevelExact)
	cw, _ := CanonMessage(whole, LevelExact)
	require.Equal(t, string(cs), string(cw))

	// 签名、密文不参与哈希
	s1 := Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockThinking, Text: "t", Signature: "x"}, {Type: BlockRedactedThinking, Data: "AAA"}}}
	s2 := Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockThinking, Text: "t", Signature: "y"}, {Type: BlockRedactedThinking, Data: "BBB"}}}
	c1, _ := CanonMessage(s1, LevelExact)
	c2, _ := CanonMessage(s2, LevelExact)
	require.Equal(t, string(c1), string(c2))

	// thinking-only 消息在 no_thinking 等级下整条跳过
	_, ok := CanonMessage(Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockThinking, Text: "t"}}}, LevelNoThinking)
	require.False(t, ok)

	// loose 等级忽略参数差异
	d := Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolCall, ID: "1", Name: "f", Arguments: json.RawMessage(`{"a":2}`)}}}
	cd, _ := CanonMessage(d, LevelExact)
	require.NotEqual(t, string(ca), string(cd))
	la, _ := CanonMessage(a, LevelLoose)
	ld, _ := CanonMessage(d, LevelLoose)
	require.Equal(t, string(la), string(ld))
}

func TestChatAdapter(t *testing.T) {
	reqBody := `{"model":"qwen3","stream":true,"temperature":0.2,
	 "messages":[{"role":"system","content":"sys"},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]},
	  {"role":"assistant","content":null,"reasoning_content":"think","tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{\"p\":1}"}}]},
	  {"role":"tool","tool_call_id":"c1","content":"a b"},{"role":"system","content":"mid reminder"}],
	 "tools":[{"type":"function","function":{"name":"ls","parameters":{"type":"object"}}}]}`
	req, err := ParseRequest(ProtocolOpenAIChat, []byte(reqBody))
	require.NoError(t, err)
	require.Equal(t, []Block{{Type: BlockText, Text: "sys"}}, req.System)
	require.Len(t, req.Messages, 4, "only leading system messages move into System")
	require.Equal(t, RoleSystem, req.Messages[3].Role)
	require.Equal(t, BlockImage, req.Messages[0].Blocks[1].Type)
	require.Equal(t, "image/png", req.Messages[0].Blocks[1].MediaType)
	require.Equal(t, Block{Type: BlockThinking, Text: "think"}, req.Messages[1].Blocks[0])
	require.Equal(t, "c1", req.Messages[1].Blocks[1].ID)
	require.Equal(t, RoleTool, req.Messages[2].Role)
	require.Equal(t, "c1", req.Messages[2].Blocks[0].CallID)
	require.Equal(t, "ls", req.Tools[0].Name)

	stream := sse(
		`{"id":"chatcmpl-1","model":"qwen3","choices":[{"delta":{"role":"assistant","reasoning_content":"step "}}]}`,
		`{"id":"chatcmpl-1","model":"qwen3","choices":[{"delta":{"reasoning_content":"one"}}]}`,
		`{"id":"chatcmpl-1","model":"qwen3","choices":[{"delta":{"content":"Done."}}]}`,
		`{"id":"chatcmpl-1","model":"qwen3","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c2","function":{"name":"ls","arguments":"{\"p\""}}]}}]}`,
		`{"id":"chatcmpl-1","model":"qwen3","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":2}"}}]}}]}`,
		`{"id":"chatcmpl-1","model":"qwen3","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-1","model":"qwen3","choices":[],"usage":{"prompt_tokens":900,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":800}}}`,
		`[DONE]`,
	)
	resp, err := ParseResponse(ProtocolOpenAIChat, stream, true)
	require.NoError(t, err)
	require.True(t, resp.Complete)
	require.Equal(t, "tool_calls", resp.StopReason)
	require.Equal(t, int64(800), resp.Usage.CacheRead)
	b := resp.Messages[0].Blocks
	require.Equal(t, Block{Type: BlockThinking, Text: "step one"}, b[0])
	require.Equal(t, "Done.", b[1].Text)
	require.JSONEq(t, `{"p":2}`, string(b[2].Arguments))

	// <think> 写在正文里的模型
	think := sse(`{"choices":[{"delta":{"content":"<think>\nhmm\n"}}]}`, `{"choices":[{"delta":{"content":"</think>\n\nAnswer"}}]}`, `[DONE]`)
	resp, err = ParseResponse(ProtocolOpenAIChat, think, true)
	require.NoError(t, err)
	require.Equal(t, []Block{{Type: BlockThinking, Text: "\nhmm\n"}, {Type: BlockText, Text: "Answer"}}, resp.Messages[0].Blocks)

	// 坏的参数 JSON 保留原文
	bad := sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c3","function":{"name":"x","arguments":"{\"trunc"}}]}}]}`)
	resp, _ = ParseResponse(ProtocolOpenAIChat, bad, true)
	require.False(t, resp.Complete)
	require.Nil(t, resp.Messages[0].Blocks[0].Arguments)
	require.Equal(t, `{"trunc`, resp.Messages[0].Blocks[0].ArgumentsRaw)
}

func TestGeminiAdapter(t *testing.T) {
	reqBody := `{"model":"gemini-3-pro","project":"p","request":{
	  "systemInstruction":{"parts":[{"text":"sys"}]},
	  "tools":[{"functionDeclarations":[{"name":"ls","description":"list","parameters":{"type":"object"}}]},{"googleSearch":{}}],
	  "contents":[{"role":"user","parts":[{"text":"hi"}]},
	    {"role":"model","parts":[{"text":"plan","thought":true},{"functionCall":{"name":"ls","args":{"p":1}},"thoughtSignature":"sig"}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"ls","response":{"out":"a"}}}]}],
	  "generationConfig":{"temperature":1}}}`
	req, err := ParseRequest(ProtocolGemini, []byte(reqBody))
	require.NoError(t, err)
	require.Equal(t, "gemini-3-pro", req.Model)
	require.Equal(t, "sys", req.System[0].Text)
	require.Len(t, req.Tools, 2)
	require.Equal(t, "ls", req.Tools[0].Name)
	require.Equal(t, RoleAssistant, req.Messages[1].Role)
	require.Equal(t, BlockThinking, req.Messages[1].Blocks[0].Type)
	require.Equal(t, BlockToolCall, req.Messages[1].Blocks[1].Type)
	require.Equal(t, BlockToolResult, req.Messages[2].Blocks[0].Type)
	require.Contains(t, req.Params, "generationConfig")

	stream := sse(
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking ","thought":true}]}}],"modelVersion":"gemini-3-pro","responseId":"r1"}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"more","thought":true}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":9,"thoughtsTokenCount":5}}}`,
	)
	resp, err := ParseResponse(ProtocolGemini, stream, true)
	require.NoError(t, err)
	require.True(t, resp.Complete)
	require.Equal(t, "r1", resp.ID)
	require.Equal(t, []Block{{Type: BlockThinking, Text: "thinking more"}, {Type: BlockText, Text: "Hello world"}}, resp.Messages[0].Blocks)
	require.Equal(t, int64(5), resp.Usage.Reasoning)

	// 非流式数组
	resp, err = ParseResponse(ProtocolGemini, []byte(`[{"candidates":[{"content":{"parts":[{"text":"a"}]}}]},{"candidates":[{"content":{"parts":[{"text":"b"}]},"finishReason":"STOP"}]}]`), false)
	require.NoError(t, err)
	require.Equal(t, "ab", resp.Messages[0].Blocks[0].Text)
}

func TestParseErrors(t *testing.T) {
	_, err := ParseRequest(ProtocolOpenAIChat, []byte("not json"))
	require.Error(t, err)
	_, err = ParseRequest("nope", []byte("{}"))
	require.Error(t, err)
	resp, err := ParseResponse(ProtocolOpenAIResponses, []byte("garbage\n\n"), true)
	require.NoError(t, err)
	require.Empty(t, resp.Messages)
	resp, err = ParseResponse(ProtocolAnthropicMessages, []byte(`{"type":"error","error":{"type":"overloaded_error"}}`), false)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"overloaded_error"}`, string(resp.Error))
}

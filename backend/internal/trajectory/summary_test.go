package trajectory

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseResponseSummaryAnthropic(t *testing.T) {
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":3000,\"cache_creation_input_tokens\":40,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":321}}\n\n"
	s := parseResponseSummary(ProtocolAnthropicMessages, []byte(sse), true)
	require.Equal(t, "msg_1", s.responseID)
	require.Equal(t, "claude-opus-5-5", s.model)
	require.Equal(t, "tool_use", s.stopReason)
	require.Equal(t, &Usage{Input: 12, Output: 321, CacheRead: 3000, CacheWrite: 40}, s.usage)

	js := `{"id":"msg_2","type":"message","model":"claude-haiku-5-5","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":6}}`
	s = parseResponseSummary(ProtocolAnthropicMessages, []byte(js), false)
	require.Equal(t, "msg_2", s.responseID)
	require.Equal(t, "end_turn", s.stopReason)
	require.Equal(t, &Usage{Input: 5, Output: 6}, s.usage)
}

func TestParseResponseSummaryOpenAIChat(t *testing.T) {
	sse := "data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-v4\",\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-v4\",\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":null}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-v4\",\"choices\":[],\"usage\":{\"prompt_tokens\":900,\"completion_tokens\":50,\"prompt_tokens_details\":{\"cached_tokens\":800},\"completion_tokens_details\":{\"reasoning_tokens\":30}}}\n\n" +
		"data: [DONE]\n\n"
	s := parseResponseSummary(ProtocolOpenAIChat, []byte(sse), true)
	require.Equal(t, "chatcmpl-1", s.responseID)
	require.Equal(t, "deepseek-v4", s.model)
	require.Equal(t, "tool_calls", s.stopReason)
	require.Equal(t, &Usage{Input: 900, Output: 50, CacheRead: 800, Reasoning: 30}, s.usage)
}

func TestParseResponseSummaryOpenAIResponsesJSON(t *testing.T) {
	js := `{"id":"resp_9","object":"response","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":20,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":8}}}`
	s := parseResponseSummary(ProtocolOpenAIResponses, []byte(js), false)
	require.Equal(t, "resp_9", s.responseID)
	require.Equal(t, "incomplete:max_output_tokens", s.stopReason)
	require.Equal(t, &Usage{Input: 10, Output: 20, CacheRead: 4, Reasoning: 8}, s.usage)
}

func TestParseResponseSummaryGemini(t *testing.T) {
	// streamGenerateContent?alt=sse
	sse := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"a\",\"thought\":true}]}}],\"modelVersion\":\"gemini-3-pro\",\"responseId\":\"r1\"}\n\n" +
		"data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":9,\"cachedContentTokenCount\":60,\"thoughtsTokenCount\":5},\"modelVersion\":\"gemini-3-pro\",\"responseId\":\"r1\"}\n\n"
	s := parseResponseSummary(ProtocolGemini, []byte(sse), true)
	require.Equal(t, "r1", s.responseID)
	require.Equal(t, "gemini-3-pro", s.model)
	require.Equal(t, "STOP", s.stopReason)
	require.Equal(t, &Usage{Input: 100, Output: 9, CacheRead: 60, Reasoning: 5}, s.usage)

	// 非 SSE 流式：JSON 数组；Antigravity 包装在 response 字段中。
	arr := `[{"response":{"responseId":"r2","modelVersion":"gemini-3-flash"}},{"response":{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2}}}]`
	s = parseResponseSummary(ProtocolGemini, []byte(arr), false)
	require.Equal(t, "r2", s.responseID)
	require.Equal(t, "gemini-3-flash", s.model)
	require.Equal(t, "MAX_TOKENS", s.stopReason)
	require.Equal(t, &Usage{Input: 7, Output: 2}, s.usage)
}

func TestParseResponseSummaryIgnoresGarbage(t *testing.T) {
	s := parseResponseSummary(ProtocolOpenAIChat, []byte("not json"), false)
	require.Nil(t, s.usage)
	s = parseResponseSummary(ProtocolOther, []byte(`{"usage":{"x":1}}`), false)
	require.Nil(t, s.usage)
}

func TestDetectProtocol(t *testing.T) {
	cases := map[string]string{
		"/v1/messages":                                      ProtocolAnthropicMessages,
		"/antigravity/v1/messages":                          ProtocolAnthropicMessages,
		"/v1/messages/count_tokens":                         ProtocolAnthropicCountTokens,
		"/v1/chat/completions":                              ProtocolOpenAIChat,
		"/chat/completions":                                 ProtocolOpenAIChat,
		"/v1/responses":                                     ProtocolOpenAIResponses,
		"/backend-api/codex/responses":                      ProtocolOpenAIResponses,
		"/v1/responses/compact":                             ProtocolOpenAIResponses,
		"/v1beta/models/gemini-3-pro:streamGenerateContent": ProtocolGemini,
		"/v1internal:streamGenerateContent":                 ProtocolGemini,
		"/v1beta/models/gemini-3-pro:countTokens":           ProtocolGeminiCountTokens,
		"/v1/embeddings":                                    ProtocolOpenAIEmbeddings,
		"/v1/images/edits":                                  ProtocolOther,
	}
	for path, want := range cases {
		require.Equal(t, want, detectProtocol(path), path)
	}
}

func TestDetectClientAndSessionHint(t *testing.T) {
	h := http.Header{"User-Agent": {"claude-cli/2.1.0 (external, cli)"}}
	require.Equal(t, "claude-code", detectClient(h))
	require.Equal(t, "codex", detectClient(http.Header{"User-Agent": {"codex_cli_rs/0.50.0"}}))
	require.Equal(t, "unknown", detectClient(http.Header{"User-Agent": {"curl/8"}}))

	hint := detectSessionHint(http.Header{"Session_id": {"abc"}}, []byte(`{"metadata":{"user_id":"x"}}`))
	require.Equal(t, &SessionHint{Source: "header:session_id", Value: "abc"}, hint)
	hint = detectSessionHint(http.Header{}, []byte(`{"metadata":{"user_id":"user_1_account_2_session_3"}}`))
	require.Equal(t, "body:metadata.user_id", hint.Source)
	require.Nil(t, detectSessionHint(http.Header{}, []byte(`{}`)))
}

func TestRequestedModelAndStream(t *testing.T) {
	require.Equal(t, "gemini-3-pro", requestedModel(ProtocolGemini, "/v1beta/models/gemini-3-pro:streamGenerateContent", nil))
	require.Equal(t, "gpt-5.5", requestedModel(ProtocolOpenAIChat, "/v1/chat/completions", []byte(`{"model":"gpt-5.5"}`)))
	require.True(t, requestWantsStream(ProtocolGemini, "/v1beta/models/g:streamGenerateContent", "", nil))
	require.True(t, requestWantsStream(ProtocolGemini, "/v1beta/models/g:generateContent", "alt=sse", nil))
	require.False(t, requestWantsStream(ProtocolOpenAIChat, "/v1/chat/completions", "", []byte(`{"stream":false}`)))
}

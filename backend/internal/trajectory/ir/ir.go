// Package ir 把四种协议（Anthropic Messages、OpenAI Chat Completions、OpenAI Responses、Gemini）
// 的请求与响应解析成统一的中间表示（design.md §5），供串联（前缀哈希 DAG）和回放使用。
//
// 原则：
//   - 不认识的块原样保存为 unknown（Raw），永不丢失；
//   - 解析是尽力而为：坏 JSON 返回错误，个别字段异常不影响其余部分；
//   - 大的二进制内容（图片 base64）不放进 IR，只保留 sha256 与字节数。
package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// 协议名与 trajectory 包的 Protocol* 常量一致。
const (
	ProtocolAnthropicMessages = "anthropic_messages"
	ProtocolOpenAIChat        = "openai_chat"
	ProtocolOpenAIResponses   = "openai_responses"
	ProtocolGemini            = "gemini"
)

// 块类型。
const (
	BlockText               = "text"
	BlockThinking           = "thinking"
	BlockRedactedThinking   = "redacted_thinking"
	BlockReasoningEncrypted = "reasoning_encrypted"
	BlockToolCall           = "tool_call"
	BlockToolResult         = "tool_result"
	BlockImage              = "image"
	BlockUnknown            = "unknown"
)

// 角色。Gemini 的 model 统一成 assistant；Responses 的 function_call_output 和 Chat 的 tool 消息统一成 tool。
const (
	RoleSystem    = "system"
	RoleDeveloper = "developer"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Block 消息中的一个内容块。
type Block struct {
	Type string `json:"type"`

	// text / thinking
	Text string `json:"text,omitempty"`
	// thinking 的签名（Anthropic signature、Gemini thoughtSignature）；不参与哈希。
	Signature string `json:"signature,omitempty"`
	// redacted_thinking / reasoning_encrypted 的密文；不参与哈希。
	Data string `json:"data,omitempty"`
	// reasoning_encrypted 附带的可读摘要（Responses reasoning.summary）。
	Summary []string `json:"summary,omitempty"`

	// tool_call
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// Arguments 合法 JSON 时为解析后的值；否则原文放在 ArgumentsRaw。
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	ArgumentsRaw string          `json:"arguments_raw,omitempty"`

	// tool_result
	CallID  string  `json:"call_id,omitempty"`
	IsError bool    `json:"is_error,omitempty"`
	Content []Block `json:"content,omitempty"`

	// image：Object 是 "sha256:<hex>"（内联数据）或 URL；Bytes 为内联数据解码前的长度。
	MediaType string `json:"media_type,omitempty"`
	Object    string `json:"object,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`

	// unknown：原始 JSON。Kind 是原始类型名（如 web_search_call、compaction）。
	Kind string          `json:"kind,omitempty"`
	Raw  json.RawMessage `json:"raw,omitempty"`
}

// Message 一条消息。Responses 的每个 input / output item 各自成为一条消息。
type Message struct {
	Role   string  `json:"role"`
	Blocks []Block `json:"blocks"`
	// ItemType Responses item 的类型（message、function_call、reasoning…），其它协议为空。
	ItemType string `json:"item_type,omitempty"`
	// ItemID Responses item 的 id（rs_…、msg_…），不参与哈希。
	ItemID string `json:"item_id,omitempty"`
}

// Tool 工具定义。非 function 工具（web_search、computer_use 等）只有 Type 和 Raw。
type Tool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// Request 统一的请求。
type Request struct {
	Protocol string    `json:"protocol"`
	Model    string    `json:"model,omitempty"`
	System   []Block   `json:"system,omitempty"`
	Tools    []Tool    `json:"tools,omitempty"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`
	// PreviousResponseID Responses 的服务端状态指针：B 中缺失的上下文要沿链展开。
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	// Params 采样等其余参数（temperature、max_tokens、reasoning、tool_choice…），原样保留。
	Params map[string]json.RawMessage `json:"params,omitempty"`
}

// Usage token 用量。
type Usage struct {
	Input      int64 `json:"input,omitempty"`
	Output     int64 `json:"output,omitempty"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
	Reasoning  int64 `json:"reasoning,omitempty"`
}

// Response 统一的响应。
type Response struct {
	Protocol   string    `json:"protocol"`
	ID         string    `json:"id,omitempty"`
	Model      string    `json:"model,omitempty"`
	Messages   []Message `json:"messages"`
	StopReason string    `json:"stop_reason,omitempty"`
	Usage      *Usage    `json:"usage,omitempty"`
	// Complete 是否见到协议的终止事件。
	Complete bool `json:"complete"`
	// Error 上游返回的错误对象（HTTP 错误体或流中的 error 事件）。
	Error json.RawMessage `json:"error,omitempty"`
}

// ParseRequest 按协议解析请求体。
func ParseRequest(protocol string, body []byte) (*Request, error) {
	switch protocol {
	case ProtocolAnthropicMessages:
		return parseAnthropicRequest(body)
	case ProtocolOpenAIChat:
		return parseChatRequest(body)
	case ProtocolOpenAIResponses:
		return parseResponsesRequest(body)
	case ProtocolGemini:
		return parseGeminiRequest(body)
	}
	return nil, &UnsupportedProtocolError{Protocol: protocol}
}

// ParseResponse 按协议解析响应体。sse=true 表示 text/event-stream，否则为 JSON（对象或 Gemini 的数组）。
func ParseResponse(protocol string, body []byte, sse bool) (*Response, error) {
	switch protocol {
	case ProtocolAnthropicMessages:
		return parseAnthropicResponse(body, sse)
	case ProtocolOpenAIChat:
		return parseChatResponse(body, sse)
	case ProtocolOpenAIResponses:
		return parseResponsesResponse(body, sse)
	case ProtocolGemini:
		return parseGeminiResponse(body, sse)
	}
	return nil, &UnsupportedProtocolError{Protocol: protocol}
}

// UnsupportedProtocolError 未知协议。
type UnsupportedProtocolError struct{ Protocol string }

func (e *UnsupportedProtocolError) Error() string { return "ir: unsupported protocol " + e.Protocol }

// ErrInvalidJSON 请求或响应体不是合法 JSON。
type ErrInvalidJSON struct{ What string }

func (e *ErrInvalidJSON) Error() string { return "ir: invalid JSON in " + e.What }

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

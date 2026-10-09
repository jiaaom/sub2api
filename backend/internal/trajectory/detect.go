package trajectory

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// 协议标识（meta.client.protocol / attempts[].upstream_protocol）。
const (
	ProtocolAnthropicMessages    = "anthropic_messages"
	ProtocolAnthropicCountTokens = "anthropic_count_tokens"
	ProtocolOpenAIChat           = "openai_chat"
	ProtocolOpenAIResponses      = "openai_responses"
	ProtocolOpenAIEmbeddings     = "openai_embeddings"
	ProtocolGemini               = "gemini"
	ProtocolGeminiCountTokens    = "gemini_count_tokens"
	ProtocolOther                = "other"
)

// detectProtocol 按 URL 路径识别协议（客户端路径与上游路径通用）。
func detectProtocol(path string) string {
	p := strings.ToLower(path)
	switch {
	case strings.HasSuffix(p, "/messages/count_tokens"):
		return ProtocolAnthropicCountTokens
	case strings.HasSuffix(p, "/messages"):
		return ProtocolAnthropicMessages
	case strings.Contains(p, "/chat/completions"):
		return ProtocolOpenAIChat
	case strings.Contains(p, "/responses"):
		return ProtocolOpenAIResponses
	case strings.HasSuffix(p, "/embeddings"):
		return ProtocolOpenAIEmbeddings
	case strings.Contains(p, ":counttokens"):
		return ProtocolGeminiCountTokens
	case strings.Contains(p, ":generatecontent"), strings.Contains(p, ":streamgeneratecontent"):
		return ProtocolGemini
	}
	return ProtocolOther
}

// 客户端识别规则：按 User-Agent / 特征 header 匹配，先命中者优先。
var clientRules = []struct {
	name  string
	match func(ua string, h http.Header) bool
}{
	{"claude-code", func(ua string, _ http.Header) bool {
		return strings.Contains(ua, "claude-cli") || strings.Contains(ua, "claude-code")
	}},
	{"codex", func(ua string, h http.Header) bool {
		return strings.Contains(ua, "codex") || h.Get("Originator") != "" && strings.Contains(strings.ToLower(h.Get("Originator")), "codex")
	}},
	{"gemini-cli", func(ua string, _ http.Header) bool {
		return strings.Contains(ua, "geminicli") || strings.Contains(ua, "gemini-cli")
	}},
	{"oh-my-pi", func(ua string, _ http.Header) bool { return strings.Contains(ua, "oh-my-pi") }},
	{"pi", func(ua string, _ http.Header) bool {
		return strings.HasPrefix(ua, "pi/") || strings.Contains(ua, "pi-coding-agent") || strings.Contains(ua, "pi-ai")
	}},
	{"opencode", func(ua string, _ http.Header) bool { return strings.Contains(ua, "opencode") }},
	{"cursor", func(ua string, _ http.Header) bool { return strings.Contains(ua, "cursor") }},
}

func detectClient(h http.Header) string {
	ua := strings.ToLower(h.Get("User-Agent"))
	for _, rule := range clientRules {
		if rule.match(ua, h) {
			return rule.name
		}
	}
	return "unknown"
}

// 会话 hint 来源：header 优先，其次 body 字段。
var sessionHeaderNames = []string{"Session_id", "Session-Id", "X-Session-Id", "Conversation_id", "Conversation-Id", "X-Claude-Code-Session-Id"}

func detectSessionHint(h http.Header, body []byte) *SessionHint {
	for _, name := range sessionHeaderNames {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			return &SessionHint{Source: "header:" + strings.ToLower(name), Value: v}
		}
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	for _, path := range []string{"metadata.user_id", "prompt_cache_key", "metadata.session_id"} {
		if v := strings.TrimSpace(gjson.GetBytes(body, path).String()); v != "" {
			return &SessionHint{Source: "body:" + path, Value: v}
		}
	}
	return nil
}

// requestedModel 客户端请求的模型：JSON body 的 model 字段，Gemini 从路径 models/{model}:action 取。
func requestedModel(protocol, path string, body []byte) string {
	if protocol == ProtocolGemini || protocol == ProtocolGeminiCountTokens {
		if i := strings.LastIndex(path, "/models/"); i >= 0 {
			rest := path[i+len("/models/"):]
			if j := strings.IndexByte(rest, ':'); j >= 0 {
				rest = rest[:j]
			}
			return rest
		}
	}
	if len(body) > 0 && gjson.ValidBytes(body) {
		return gjson.GetBytes(body, "model").String()
	}
	return ""
}

func requestWantsStream(protocol, path, rawQuery string, body []byte) bool {
	if protocol == ProtocolGemini {
		if strings.Contains(strings.ToLower(path), ":streamgeneratecontent") {
			return true
		}
		q, _ := url.ParseQuery(rawQuery)
		return q.Get("alt") == "sse"
	}
	if len(body) > 0 && gjson.ValidBytes(body) {
		return gjson.GetBytes(body, "stream").Bool()
	}
	return false
}

func mediaType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(contentType))
	}
	return mt
}

// bodyExt 请求 body 的文件扩展名。
func bodyExt(contentType string, body []byte) string {
	mt := mediaType(contentType)
	switch {
	case strings.Contains(mt, "json"):
		return ".json"
	case mt == "" && len(body) > 0 && gjson.ValidBytes(body):
		return ".json"
	case strings.HasPrefix(mt, "multipart/"):
		return ".multipart"
	case strings.HasPrefix(mt, "text/"):
		return ".txt"
	}
	return ".bin"
}

// responseExt 响应 body 的文件扩展名。
func responseExt(contentType string) string {
	mt := mediaType(contentType)
	switch {
	case mt == "text/event-stream":
		return ".sse"
	case strings.Contains(mt, "json"):
		return ".json"
	case strings.HasPrefix(mt, "text/"):
		return ".txt"
	}
	return ".bin"
}

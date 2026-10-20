package trajectory

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
)

// summarizeExchange 尽力从最终 attempt 的 B/C 中解析模型、usage、response id 等摘要写入 meta。
// 解析失败不影响落定；raw 文件始终是事实来源。
func summarizeExchange(dir string, m *Meta) {
	att := summaryAttempt(m)
	if att == nil {
		return
	}
	if att.RequestFile != "" {
		if body, err := os.ReadFile(filepath.Join(dir, att.RequestFile)); err == nil && gjson.ValidBytes(body) {
			if v := gjson.GetBytes(body, "previous_response_id").String(); v != "" {
				m.PreviousResponseID = v
			}
		}
	}
	if att.ResponseFile == "" {
		return
	}
	body, err := os.ReadFile(filepath.Join(dir, att.ResponseFile))
	if err != nil || len(body) == 0 {
		return
	}
	s := parseResponseSummary(att.UpstreamProtocol, body, isSSEFile(att.ResponseFile, body))
	if s.model != "" {
		m.Model.Upstream = s.model
	}
	if s.usage != nil {
		m.Usage = s.usage
	}
	if s.responseID != "" {
		m.ResponseID = s.responseID
	}
	if s.stopReason != "" {
		m.StopReason = s.stopReason
	}
}

// summaryAttempt 优先最终成功的 attempt，否则最后一个有响应的 attempt。
func summaryAttempt(m *Meta) *AttemptMeta {
	if m.FinalAttempt != nil && *m.FinalAttempt < len(m.Attempts) {
		return &m.Attempts[*m.FinalAttempt]
	}
	for i := len(m.Attempts) - 1; i >= 0; i-- {
		if m.Attempts[i].ResponseFile != "" {
			return &m.Attempts[i]
		}
	}
	return nil
}

type responseSummary struct {
	model      string
	responseID string
	stopReason string
	usage      *Usage
	// terminal 是否见到协议的终止事件（response.completed、message_stop、[DONE]、finishReason 等），
	// 即响应在协议层面已完整，此后的断连不影响数据完整性。
	terminal bool
}

// parseResponseSummary 按协议解析响应；sse=true 时逐个 data: 事件解析，否则按 JSON（对象或数组）解析。
func parseResponseSummary(protocol string, body []byte, sse bool) responseSummary {
	var s responseSummary
	visit := func(doc gjson.Result) {
		switch protocol {
		case ProtocolAnthropicMessages:
			s.visitAnthropic(doc)
		case ProtocolOpenAIChat:
			s.visitOpenAIChat(doc)
		case ProtocolOpenAIResponses:
			s.visitOpenAIResponses(doc)
		case ProtocolGemini:
			s.visitGemini(doc)
		}
	}
	if sse {
		forEachSSEData(body, func(data []byte) {
			if bytes.Equal(data, []byte("[DONE]")) {
				s.terminal = true
				return
			}
			if gjson.ValidBytes(data) {
				visit(gjson.ParseBytes(data))
			}
		})
		return s
	}
	if !gjson.ValidBytes(body) {
		return s
	}
	doc := gjson.ParseBytes(body)
	if doc.IsArray() {
		doc.ForEach(func(_, v gjson.Result) bool { visit(v); return true })
	} else {
		visit(doc)
	}
	return s
}

func forEachSSEData(body []byte, fn func([]byte)) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	var data []byte
	flush := func() {
		if len(data) > 0 {
			fn(data)
			data = nil
		}
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			flush()
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			rest = bytes.TrimPrefix(rest, []byte(" "))
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, rest...)
		}
	}
	flush()
}

func (s *responseSummary) ensureUsage() *Usage {
	if s.usage == nil {
		s.usage = &Usage{}
	}
	return s.usage
}

func setIfPositive(dst *int64, v gjson.Result) {
	if v.Exists() && v.Int() > 0 {
		*dst = v.Int()
	}
}

// Anthropic：message_start.message / message_delta / 非流式整体对象。
func (s *responseSummary) visitAnthropic(doc gjson.Result) {
	msg := doc
	if doc.Get("type").String() == "message_start" {
		msg = doc.Get("message")
	}
	if v := msg.Get("id").String(); v != "" && msg.Get("type").String() == "message" {
		s.responseID = v
	}
	if v := msg.Get("model").String(); v != "" {
		s.model = v
	}
	if v := msg.Get("stop_reason").String(); v != "" {
		s.stopReason = v
	}
	if v := doc.Get("delta.stop_reason").String(); v != "" {
		s.stopReason = v
	}
	if t := doc.Get("type").String(); t == "message_stop" || (t == "message" && msg.Get("stop_reason").String() != "") {
		s.terminal = true
	}
	usage := msg.Get("usage")
	if !usage.Exists() {
		usage = doc.Get("usage")
	}
	if usage.Exists() {
		u := s.ensureUsage()
		setIfPositive(&u.Input, usage.Get("input_tokens"))
		setIfPositive(&u.Output, usage.Get("output_tokens"))
		setIfPositive(&u.CacheRead, usage.Get("cache_read_input_tokens"))
		setIfPositive(&u.CacheWrite, usage.Get("cache_creation_input_tokens"))
	}
}

// OpenAI Chat Completions：chunk 或整体对象；usage 在最后一个 chunk（stream_options.include_usage）。
func (s *responseSummary) visitOpenAIChat(doc gjson.Result) {
	if v := doc.Get("id").String(); v != "" {
		s.responseID = v
	}
	if v := doc.Get("model").String(); v != "" {
		s.model = v
	}
	if v := doc.Get("choices.0.finish_reason").String(); v != "" {
		s.stopReason = v
		s.terminal = true
	}
	if usage := doc.Get("usage"); usage.Exists() && usage.Type != gjson.Null {
		u := s.ensureUsage()
		setIfPositive(&u.Input, usage.Get("prompt_tokens"))
		setIfPositive(&u.Output, usage.Get("completion_tokens"))
		setIfPositive(&u.CacheRead, usage.Get("prompt_tokens_details.cached_tokens"))
		setIfPositive(&u.Reasoning, usage.Get("completion_tokens_details.reasoning_tokens"))
	}
}

// OpenAI Responses：response.* 事件中的 response 对象，或非流式整体对象。
func (s *responseSummary) visitOpenAIResponses(doc gjson.Result) {
	resp := doc
	if r := doc.Get("response"); r.Exists() && r.IsObject() {
		resp = r
	}
	if resp.Get("object").String() != "response" && !strings.HasPrefix(resp.Get("id").String(), "resp") {
		return
	}
	if v := resp.Get("id").String(); v != "" {
		s.responseID = v
	}
	if v := resp.Get("model").String(); v != "" {
		s.model = v
	}
	if v := resp.Get("status").String(); v != "" && v != "in_progress" && v != "queued" {
		s.terminal = true
		s.stopReason = v
		if reason := resp.Get("incomplete_details.reason").String(); reason != "" {
			s.stopReason = v + ":" + reason
		}
	}
	if usage := resp.Get("usage"); usage.Exists() && usage.Type != gjson.Null {
		u := s.ensureUsage()
		setIfPositive(&u.Input, usage.Get("input_tokens"))
		setIfPositive(&u.Output, usage.Get("output_tokens"))
		setIfPositive(&u.CacheRead, usage.Get("input_tokens_details.cached_tokens"))
		setIfPositive(&u.Reasoning, usage.Get("output_tokens_details.reasoning_tokens"))
	}
}

// Gemini（含 Antigravity / Code Assist 的 {"response": {...}} 包装）。
func (s *responseSummary) visitGemini(doc gjson.Result) {
	if r := doc.Get("response"); r.Exists() && r.IsObject() {
		doc = r
	}
	if v := doc.Get("responseId").String(); v != "" {
		s.responseID = v
	}
	if v := doc.Get("modelVersion").String(); v != "" {
		s.model = v
	}
	if v := doc.Get("candidates.0.finishReason").String(); v != "" {
		s.stopReason = v
		s.terminal = true
	}
	if usage := doc.Get("usageMetadata"); usage.Exists() {
		u := s.ensureUsage()
		setIfPositive(&u.Input, usage.Get("promptTokenCount"))
		setIfPositive(&u.Output, usage.Get("candidatesTokenCount"))
		setIfPositive(&u.CacheRead, usage.Get("cachedContentTokenCount"))
		setIfPositive(&u.Reasoning, usage.Get("thoughtsTokenCount"))
	}
}

// responseFileTerminal 判断已落盘的响应文件是否包含协议终止事件。
func responseFileTerminal(dir, protocol, file string) bool {
	if file == "" {
		return false
	}
	body, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil || len(body) == 0 {
		return false
	}
	return parseResponseSummary(protocol, body, isSSEFile(file, body)).terminal
}

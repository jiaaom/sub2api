package ir

import (
	"encoding/json"
	"sort"

	"github.com/tidwall/gjson"
)

func parseAnthropicRequest(body []byte) (*Request, error) {
	if !gjson.ValidBytes(body) {
		return nil, &ErrInvalidJSON{What: "anthropic request"}
	}
	doc := gjson.ParseBytes(body)
	req := &Request{
		Protocol: ProtocolAnthropicMessages,
		Model:    doc.Get("model").String(),
		Stream:   doc.Get("stream").Bool(),
		Messages: []Message{},
		Params:   params(doc, "model", "stream", "system", "tools", "messages"),
	}
	req.System = contentBlocks(doc.Get("system"), anthropicBlock)
	each(doc.Get("tools"), func(_, t gjson.Result) bool {
		if t.Get("input_schema").Exists() || t.Get("type").String() == "custom" || !t.Get("type").Exists() {
			req.Tools = append(req.Tools, Tool{Type: "function", Name: t.Get("name").String(), Description: t.Get("description").String(), Schema: rawJSON(t.Get("input_schema"))})
		} else {
			req.Tools = append(req.Tools, Tool{Type: t.Get("type").String(), Name: t.Get("name").String(), Raw: rawJSON(t)})
		}
		return true
	})
	each(doc.Get("messages"), func(_, m gjson.Result) bool {
		req.Messages = append(req.Messages, Message{Role: m.Get("role").String(), Blocks: contentBlocks(m.Get("content"), anthropicBlock)})
		return true
	})
	return req, nil
}

func anthropicBlock(b gjson.Result) Block {
	switch typ := b.Get("type").String(); typ {
	case "text":
		return Block{Type: BlockText, Text: b.Get("text").String()}
	case "thinking":
		return Block{Type: BlockThinking, Text: b.Get("thinking").String(), Signature: b.Get("signature").String()}
	case "redacted_thinking":
		return Block{Type: BlockRedactedThinking, Data: b.Get("data").String()}
	case "tool_use":
		return Block{Type: BlockToolCall, ID: b.Get("id").String(), Name: b.Get("name").String(), Arguments: inputOrEmpty(b.Get("input"))}
	case "tool_result":
		return Block{
			Type:    BlockToolResult,
			CallID:  b.Get("tool_use_id").String(),
			IsError: b.Get("is_error").Bool(),
			Content: contentBlocks(b.Get("content"), anthropicBlock),
		}
	case "image":
		src := b.Get("source")
		if src.Get("type").String() == "base64" {
			return imageBlock(src.Get("media_type").String(), src.Get("data").String())
		}
		return Block{Type: BlockImage, Object: src.Get("url").String(), MediaType: src.Get("media_type").String()}
	default:
		return unknownBlock(typ, b)
	}
}

func inputOrEmpty(v gjson.Result) json.RawMessage {
	if !v.Exists() || v.Type == gjson.Null {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(v.Raw)
}

// anthropicStream 按 content block 的 index 拼接流式事件。
type anthropicStream struct {
	blocks   map[int]*Block
	partial  map[int]string // input_json_delta 累积
	order    []int
	resp     *Response
	hasStart bool
}

func parseAnthropicResponse(body []byte, sse bool) (*Response, error) {
	resp := &Response{Protocol: ProtocolAnthropicMessages, Messages: []Message{}}
	if !sse {
		if !gjson.ValidBytes(body) {
			return nil, &ErrInvalidJSON{What: "anthropic response"}
		}
		doc := gjson.ParseBytes(body)
		if doc.Get("type").String() == "error" {
			resp.Error = rawJSON(doc.Get("error"))
			return resp, nil
		}
		anthropicMessageMeta(resp, doc)
		resp.Messages = append(resp.Messages, Message{Role: RoleAssistant, Blocks: contentBlocks(doc.Get("content"), anthropicBlock)})
		resp.Complete = doc.Get("stop_reason").String() != ""
		return resp, nil
	}
	st := &anthropicStream{blocks: map[int]*Block{}, partial: map[int]string{}, resp: resp}
	forEachJSONEvent(body, func(_ string, ev gjson.Result) { st.event(ev) }, nil)
	msg := Message{Role: RoleAssistant, Blocks: []Block{}}
	sort.Ints(st.order)
	for _, i := range st.order {
		b := st.blocks[i]
		if b.Type == BlockToolCall {
			if raw, ok := st.partial[i]; ok {
				b.Arguments, b.ArgumentsRaw = toolArguments(raw)
			}
		}
		msg.Blocks = append(msg.Blocks, *b)
	}
	if st.hasStart || len(msg.Blocks) > 0 {
		resp.Messages = append(resp.Messages, msg)
	}
	return resp, nil
}

func (st *anthropicStream) event(ev gjson.Result) {
	switch ev.Get("type").String() {
	case "message_start":
		st.hasStart = true
		anthropicMessageMeta(st.resp, ev.Get("message"))
	case "content_block_start":
		i := int(ev.Get("index").Int())
		b := anthropicBlock(ev.Get("content_block"))
		if b.Type == BlockToolCall {
			b.Arguments = nil // 以 input_json_delta 为准
			st.partial[i] = ""
		}
		st.blocks[i] = &b
		st.order = append(st.order, i)
	case "content_block_delta":
		i := int(ev.Get("index").Int())
		b := st.blocks[i]
		if b == nil {
			b = &Block{Type: BlockText}
			st.blocks[i] = b
			st.order = append(st.order, i)
		}
		d := ev.Get("delta")
		switch d.Get("type").String() {
		case "text_delta":
			b.Text += d.Get("text").String()
		case "thinking_delta":
			b.Text += d.Get("thinking").String()
		case "signature_delta":
			b.Signature += d.Get("signature").String()
		case "input_json_delta":
			st.partial[i] += d.Get("partial_json").String()
		}
	case "message_delta":
		if v := ev.Get("delta.stop_reason").String(); v != "" {
			st.resp.StopReason = v
		}
		anthropicUsage(st.resp, ev.Get("usage"))
	case "message_stop":
		st.resp.Complete = true
	case "error":
		st.resp.Error = rawJSON(ev.Get("error"))
	}
}

func anthropicMessageMeta(resp *Response, msg gjson.Result) {
	if v := msg.Get("id").String(); v != "" {
		resp.ID = v
	}
	if v := msg.Get("model").String(); v != "" {
		resp.Model = v
	}
	if v := msg.Get("stop_reason").String(); v != "" {
		resp.StopReason = v
	}
	anthropicUsage(resp, msg.Get("usage"))
}

func anthropicUsage(resp *Response, u gjson.Result) {
	if !u.Exists() || u.Type == gjson.Null {
		return
	}
	if resp.Usage == nil {
		resp.Usage = &Usage{}
	}
	setUsage(&resp.Usage.Input, u.Get("input_tokens"))
	setUsage(&resp.Usage.Output, u.Get("output_tokens"))
	setUsage(&resp.Usage.CacheRead, u.Get("cache_read_input_tokens"))
	setUsage(&resp.Usage.CacheWrite, u.Get("cache_creation_input_tokens"))
}

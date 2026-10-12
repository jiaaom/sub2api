package ir

import (
	"sort"

	"github.com/tidwall/gjson"
)

func parseChatRequest(body []byte) (*Request, error) {
	if !gjson.ValidBytes(body) {
		return nil, &ErrInvalidJSON{What: "chat request"}
	}
	doc := gjson.ParseBytes(body)
	req := &Request{
		Protocol: ProtocolOpenAIChat,
		Model:    doc.Get("model").String(),
		Stream:   doc.Get("stream").Bool(),
		Messages: []Message{},
		Params:   params(doc, "model", "stream", "messages", "tools", "functions"),
	}
	each(doc.Get("tools"), func(_, t gjson.Result) bool {
		if fn := t.Get("function"); fn.Exists() {
			req.Tools = append(req.Tools, Tool{Type: "function", Name: fn.Get("name").String(), Description: fn.Get("description").String(), Schema: rawJSON(fn.Get("parameters"))})
		} else {
			req.Tools = append(req.Tools, Tool{Type: t.Get("type").String(), Raw: rawJSON(t)})
		}
		return true
	})
	leading := true // 开头连续的 system / developer 消息归入 System（h0）
	each(doc.Get("messages"), func(_, m gjson.Result) bool {
		msg := chatMessage(m)
		if leading && (msg.Role == RoleSystem || msg.Role == RoleDeveloper) {
			req.System = append(req.System, msg.Blocks...)
			return true
		}
		leading = false
		req.Messages = append(req.Messages, msg)
		return true
	})
	return req, nil
}

func chatMessage(m gjson.Result) Message {
	role := m.Get("role").String()
	msg := Message{Role: role, Blocks: []Block{}}
	if role == RoleAssistant {
		if r := chatReasoning(m); r != "" {
			msg.Blocks = append(msg.Blocks, Block{Type: BlockThinking, Text: r})
		}
	}
	content := contentBlocks(m.Get("content"), chatPart)
	if role == RoleAssistant {
		content = liftThink(content)
	}
	if role == RoleTool || role == "function" {
		msg.Role = RoleTool
		msg.Blocks = append(msg.Blocks, Block{Type: BlockToolResult, CallID: m.Get("tool_call_id").String(), Content: content})
		return msg
	}
	msg.Blocks = append(msg.Blocks, content...)
	each(m.Get("tool_calls"), func(_, tc gjson.Result) bool {
		args, raw := toolArguments(tc.Get("function.arguments").String())
		msg.Blocks = append(msg.Blocks, Block{Type: BlockToolCall, ID: tc.Get("id").String(), Name: tc.Get("function.name").String(), Arguments: args, ArgumentsRaw: raw})
		return true
	})
	return msg
}

// chatReasoning CoT 提取顺序：reasoning_content → reasoning（design.md §5）。
func chatReasoning(m gjson.Result) string {
	if v := m.Get("reasoning_content").String(); v != "" {
		return v
	}
	if r := m.Get("reasoning"); r.Type == gjson.String {
		return r.String()
	}
	return ""
}

func chatPart(p gjson.Result) Block {
	switch typ := p.Get("type").String(); typ {
	case "text":
		return Block{Type: BlockText, Text: p.Get("text").String()}
	case "image_url":
		b := imageFromURL(p.Get("image_url.url").String())
		return b
	default:
		return unknownBlock(typ, p)
	}
}

type chatToolAcc struct {
	id, name, args string
}

func parseChatResponse(body []byte, sse bool) (*Response, error) {
	resp := &Response{Protocol: ProtocolOpenAIChat, Messages: []Message{}}
	if !sse {
		if !gjson.ValidBytes(body) {
			return nil, &ErrInvalidJSON{What: "chat response"}
		}
		doc := gjson.ParseBytes(body)
		if e := doc.Get("error"); e.Exists() {
			resp.Error = rawJSON(e)
			return resp, nil
		}
		chatMeta(resp, doc)
		if m := doc.Get("choices.0.message"); m.Exists() {
			msg := chatMessage(m)
			msg.Role = RoleAssistant
			resp.Messages = append(resp.Messages, msg)
		}
		resp.Complete = resp.StopReason != ""
		return resp, nil
	}
	var thinking, text string
	tools := map[int]*chatToolAcc{}
	var toolOrder []int
	seen := false
	forEachJSONEvent(body, func(_ string, ev gjson.Result) {
		if e := ev.Get("error"); e.Exists() {
			resp.Error = rawJSON(e)
			return
		}
		chatMeta(resp, ev)
		if resp.StopReason != "" {
			resp.Complete = true
		}
		d := ev.Get("choices.0.delta")
		if !d.Exists() {
			return
		}
		seen = true
		if v := d.Get("reasoning_content").String(); v != "" {
			thinking += v
		} else if r := d.Get("reasoning"); r.Type == gjson.String {
			thinking += r.String()
		}
		text += d.Get("content").String()
		each(d.Get("tool_calls"), func(_, tc gjson.Result) bool {
			i := int(tc.Get("index").Int())
			acc := tools[i]
			if acc == nil {
				acc = &chatToolAcc{}
				tools[i] = acc
				toolOrder = append(toolOrder, i)
			}
			if v := tc.Get("id").String(); v != "" {
				acc.id = v
			}
			if v := tc.Get("function.name").String(); v != "" {
				acc.name = v
			}
			acc.args += tc.Get("function.arguments").String()
			return true
		})
	}, func() { resp.Complete = true })
	if !seen {
		return resp, nil
	}
	msg := Message{Role: RoleAssistant, Blocks: []Block{}}
	if thinking != "" {
		msg.Blocks = append(msg.Blocks, Block{Type: BlockThinking, Text: thinking})
	}
	if text != "" {
		msg.Blocks = append(msg.Blocks, liftThink([]Block{{Type: BlockText, Text: text}})...)
	}
	sort.Ints(toolOrder)
	for _, i := range toolOrder {
		acc := tools[i]
		args, raw := toolArguments(acc.args)
		msg.Blocks = append(msg.Blocks, Block{Type: BlockToolCall, ID: acc.id, Name: acc.name, Arguments: args, ArgumentsRaw: raw})
	}
	resp.Messages = append(resp.Messages, msg)
	return resp, nil
}

func chatMeta(resp *Response, doc gjson.Result) {
	if v := doc.Get("id").String(); v != "" {
		resp.ID = v
	}
	if v := doc.Get("model").String(); v != "" {
		resp.Model = v
	}
	if v := doc.Get("choices.0.finish_reason").String(); v != "" {
		resp.StopReason = v
	}
	if u := doc.Get("usage"); u.Exists() && u.Type != gjson.Null {
		if resp.Usage == nil {
			resp.Usage = &Usage{}
		}
		setUsage(&resp.Usage.Input, u.Get("prompt_tokens"))
		setUsage(&resp.Usage.Output, u.Get("completion_tokens"))
		setUsage(&resp.Usage.CacheRead, u.Get("prompt_tokens_details.cached_tokens"))
		setUsage(&resp.Usage.Reasoning, u.Get("completion_tokens_details.reasoning_tokens"))
	}
}

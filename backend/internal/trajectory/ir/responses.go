package ir

import (
	"sort"

	"github.com/tidwall/gjson"
)

func parseResponsesRequest(body []byte) (*Request, error) {
	if !gjson.ValidBytes(body) {
		return nil, &ErrInvalidJSON{What: "responses request"}
	}
	doc := gjson.ParseBytes(body)
	req := &Request{
		Protocol:           ProtocolOpenAIResponses,
		Model:              doc.Get("model").String(),
		Stream:             doc.Get("stream").Bool(),
		PreviousResponseID: doc.Get("previous_response_id").String(),
		Messages:           []Message{},
		Params:             params(doc, "model", "stream", "instructions", "input", "tools", "previous_response_id"),
	}
	if v := doc.Get("instructions"); v.Exists() {
		req.System = contentBlocks(v, responsesPart)
	}
	each(doc.Get("tools"), func(_, t gjson.Result) bool {
		if t.Get("type").String() == "function" {
			req.Tools = append(req.Tools, Tool{Type: "function", Name: t.Get("name").String(), Description: t.Get("description").String(), Schema: rawJSON(t.Get("parameters"))})
		} else {
			req.Tools = append(req.Tools, Tool{Type: t.Get("type").String(), Name: t.Get("name").String(), Raw: rawJSON(t)})
		}
		return true
	})
	input := doc.Get("input")
	if input.Type == gjson.String {
		req.Messages = append(req.Messages, Message{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: input.String()}}})
		return req, nil
	}
	each(input, func(_, item gjson.Result) bool {
		req.Messages = append(req.Messages, responsesItem(item))
		return true
	})
	return req, nil
}

// responsesItem 一个 input / output item 转为一条消息。
func responsesItem(item gjson.Result) Message {
	typ := item.Get("type").String()
	if typ == "" && item.Get("role").Exists() {
		typ = "message"
	}
	msg := Message{ItemType: typ, ItemID: item.Get("id").String(), Blocks: []Block{}}
	switch typ {
	case "message":
		msg.Role = item.Get("role").String()
		msg.Blocks = contentBlocks(item.Get("content"), responsesPart)
		if msg.Blocks == nil {
			msg.Blocks = []Block{}
		}
	case "function_call":
		msg.Role = RoleAssistant
		args, raw := toolArguments(item.Get("arguments").String())
		msg.Blocks = append(msg.Blocks, Block{Type: BlockToolCall, ID: item.Get("call_id").String(), Name: item.Get("name").String(), Arguments: args, ArgumentsRaw: raw})
	case "custom_tool_call":
		msg.Role = RoleAssistant
		msg.Blocks = append(msg.Blocks, Block{Type: BlockToolCall, ID: item.Get("call_id").String(), Name: item.Get("name").String(), ArgumentsRaw: item.Get("input").String()})
	case "function_call_output", "custom_tool_call_output":
		msg.Role = RoleTool
		msg.Blocks = append(msg.Blocks, Block{Type: BlockToolResult, CallID: item.Get("call_id").String(), Content: contentBlocks(item.Get("output"), responsesPart)})
	case "reasoning":
		msg.Role = RoleAssistant
		var text string
		each(item.Get("content"), func(_, c gjson.Result) bool {
			text += c.Get("text").String()
			return true
		})
		var summary []string
		each(item.Get("summary"), func(_, s gjson.Result) bool {
			summary = append(summary, s.Get("text").String())
			return true
		})
		if text != "" {
			msg.Blocks = append(msg.Blocks, Block{Type: BlockThinking, Text: text, Summary: summary})
		}
		if enc := item.Get("encrypted_content").String(); enc != "" {
			msg.Blocks = append(msg.Blocks, Block{Type: BlockReasoningEncrypted, Data: enc, Summary: summary})
		} else if text == "" && len(summary) > 0 {
			msg.Blocks = append(msg.Blocks, Block{Type: BlockThinking, Text: joinSummary(summary), Summary: summary})
		}
	default:
		// compaction、web_search_call、local_shell_call、item_reference… 原样保留
		msg.Role = RoleAssistant
		if typ == "item_reference" || typ == "local_shell_call_output" || typ == "computer_call_output" {
			msg.Role = RoleTool
		}
		msg.Blocks = append(msg.Blocks, unknownBlock(typ, item))
	}
	return msg
}

func joinSummary(s []string) string {
	out := ""
	for i, p := range s {
		if i > 0 {
			out += "\n\n"
		}
		out += p
	}
	return out
}

func responsesPart(p gjson.Result) Block {
	switch typ := p.Get("type").String(); typ {
	case "input_text", "output_text", "text", "summary_text", "reasoning_text":
		return Block{Type: BlockText, Text: p.Get("text").String()}
	case "refusal":
		return Block{Type: BlockText, Text: p.Get("refusal").String()}
	case "input_image":
		if url := p.Get("image_url").String(); url != "" {
			return imageFromURL(url)
		}
		return unknownBlock(typ, p)
	default:
		return unknownBlock(typ, p)
	}
}

// responsesStream 流式拼接：优先 response.completed 中的完整 output；
// 否则用 output_item.done；再否则用增量事件按 output_index 拼。
type responsesStream struct {
	resp     *Response
	final    gjson.Result
	done     map[int]gjson.Result
	partial  map[int]*Message
	order    []int
	argsAcc  map[int]string
	itemSeen map[int]bool
}

func parseResponsesResponse(body []byte, sse bool) (*Response, error) {
	resp := &Response{Protocol: ProtocolOpenAIResponses, Messages: []Message{}}
	if !sse {
		if !gjson.ValidBytes(body) {
			return nil, &ErrInvalidJSON{What: "responses response"}
		}
		doc := gjson.ParseBytes(body)
		if e := doc.Get("error"); e.Exists() && e.Type != gjson.Null {
			resp.Error = rawJSON(e)
		}
		responsesMeta(resp, doc)
		each(doc.Get("output"), func(_, item gjson.Result) bool {
			resp.Messages = append(resp.Messages, responsesItem(item))
			return true
		})
		return resp, nil
	}
	st := &responsesStream{resp: resp, done: map[int]gjson.Result{}, partial: map[int]*Message{}, argsAcc: map[int]string{}, itemSeen: map[int]bool{}}
	forEachJSONEvent(body, func(_ string, ev gjson.Result) { st.event(ev) }, nil)
	if st.final.Exists() && st.final.Get("output.#").Int() > 0 {
		each(st.final.Get("output"), func(_, item gjson.Result) bool {
			resp.Messages = append(resp.Messages, responsesItem(item))
			return true
		})
		return resp, nil
	}
	sort.Ints(st.order)
	for _, i := range st.order {
		if item, ok := st.done[i]; ok {
			resp.Messages = append(resp.Messages, responsesItem(item))
			continue
		}
		if m := st.partial[i]; m != nil {
			if raw, ok := st.argsAcc[i]; ok {
				for j := range m.Blocks {
					if m.Blocks[j].Type == BlockToolCall {
						m.Blocks[j].Arguments, m.Blocks[j].ArgumentsRaw = toolArguments(raw)
					}
				}
			}
			resp.Messages = append(resp.Messages, *m)
		}
	}
	return resp, nil
}

func (st *responsesStream) touch(i int) {
	if !st.itemSeen[i] {
		st.itemSeen[i] = true
		st.order = append(st.order, i)
	}
}

func (st *responsesStream) event(ev gjson.Result) {
	typ := ev.Get("type").String()
	if r := ev.Get("response"); r.Exists() && r.IsObject() {
		responsesMeta(st.resp, r)
		switch typ {
		case "response.completed", "response.incomplete", "response.failed", "response.done":
			st.final = r
		}
		return
	}
	i := int(ev.Get("output_index").Int())
	switch typ {
	case "response.output_item.added":
		st.touch(i)
		m := responsesItem(ev.Get("item"))
		st.partial[i] = &m
	case "response.output_item.done":
		st.touch(i)
		st.done[i] = ev.Get("item")
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		st.touch(i)
		m := st.partial[i]
		if m == nil {
			m = &Message{Role: RoleAssistant, Blocks: []Block{}}
			st.partial[i] = m
		}
		blockType := BlockText
		if typ != "response.output_text.delta" {
			blockType = BlockThinking
		}
		m.Blocks = appendText(m.Blocks, blockType, ev.Get("delta").String())
	case "response.function_call_arguments.delta":
		st.touch(i)
		st.argsAcc[i] += ev.Get("delta").String()
	case "error":
		st.resp.Error = rawJSON(ev)
	}
}

func responsesMeta(resp *Response, r gjson.Result) {
	if v := r.Get("id").String(); v != "" {
		resp.ID = v
	}
	if v := r.Get("model").String(); v != "" {
		resp.Model = v
	}
	if v := r.Get("status").String(); v != "" && v != "in_progress" && v != "queued" {
		resp.Complete = true
		resp.StopReason = v
		if reason := r.Get("incomplete_details.reason").String(); reason != "" {
			resp.StopReason = v + ":" + reason
		}
	}
	if e := r.Get("error"); e.Exists() && e.Type != gjson.Null {
		resp.Error = rawJSON(e)
	}
	if u := r.Get("usage"); u.Exists() && u.Type != gjson.Null {
		if resp.Usage == nil {
			resp.Usage = &Usage{}
		}
		setUsage(&resp.Usage.Input, u.Get("input_tokens"))
		setUsage(&resp.Usage.Output, u.Get("output_tokens"))
		setUsage(&resp.Usage.CacheRead, u.Get("input_tokens_details.cached_tokens"))
		setUsage(&resp.Usage.Reasoning, u.Get("output_tokens_details.reasoning_tokens"))
	}
}

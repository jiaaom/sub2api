package ir

import (
	"github.com/tidwall/gjson"
)

func parseGeminiRequest(body []byte) (*Request, error) {
	if !gjson.ValidBytes(body) {
		return nil, &ErrInvalidJSON{What: "gemini request"}
	}
	doc := gjson.ParseBytes(body)
	model := doc.Get("model").String()
	// Antigravity / Code Assist 包装：{"model": ..., "project": ..., "request": {...}}
	if inner := doc.Get("request"); inner.IsObject() && inner.Get("contents").Exists() {
		doc = inner
	}
	req := &Request{
		Protocol: ProtocolGemini,
		Model:    model,
		Messages: []Message{},
		Params:   params(doc, "contents", "systemInstruction", "system_instruction", "tools"),
	}
	sys := doc.Get("systemInstruction")
	if !sys.Exists() {
		sys = doc.Get("system_instruction")
	}
	req.System = geminiParts(sys.Get("parts"))
	each(doc.Get("tools"), func(_, t gjson.Result) bool {
		decls := t.Get("functionDeclarations")
		if !decls.Exists() {
			decls = t.Get("function_declarations")
		}
		if decls.Exists() {
			each(decls, func(_, d gjson.Result) bool {
				schema := d.Get("parameters")
				if !schema.Exists() {
					schema = d.Get("parametersJsonSchema")
				}
				req.Tools = append(req.Tools, Tool{Type: "function", Name: d.Get("name").String(), Description: d.Get("description").String(), Schema: rawJSON(schema)})
				return true
			})
			return true
		}
		req.Tools = append(req.Tools, Tool{Raw: rawJSON(t)})
		return true
	})
	each(doc.Get("contents"), func(_, c gjson.Result) bool {
		req.Messages = append(req.Messages, Message{Role: geminiRole(c.Get("role").String()), Blocks: geminiParts(c.Get("parts"))})
		return true
	})
	return req, nil
}

func geminiRole(role string) string {
	if role == "model" {
		return RoleAssistant
	}
	if role == "" {
		return RoleUser
	}
	return role
}

func geminiParts(parts gjson.Result) []Block {
	out := []Block{}
	each(parts, func(_, p gjson.Result) bool {
		out = append(out, geminiPart(p))
		return true
	})
	return out
}

func geminiPart(p gjson.Result) Block {
	switch {
	case p.Get("functionCall").Exists():
		fc := p.Get("functionCall")
		return Block{Type: BlockToolCall, ID: fc.Get("id").String(), Name: fc.Get("name").String(), Arguments: inputOrEmpty(fc.Get("args")), Signature: p.Get("thoughtSignature").String()}
	case p.Get("functionResponse").Exists():
		fr := p.Get("functionResponse")
		return Block{Type: BlockToolResult, CallID: fr.Get("id").String(), Name: fr.Get("name").String(), Content: []Block{{Type: BlockUnknown, Kind: "function_response", Raw: rawJSON(fr.Get("response"))}}}
	case p.Get("inlineData").Exists():
		d := p.Get("inlineData")
		return imageBlock(d.Get("mimeType").String(), d.Get("data").String())
	case p.Get("text").Exists():
		if p.Get("thought").Bool() {
			return Block{Type: BlockThinking, Text: p.Get("text").String(), Signature: p.Get("thoughtSignature").String()}
		}
		return Block{Type: BlockText, Text: p.Get("text").String(), Signature: p.Get("thoughtSignature").String()}
	default:
		return unknownBlock("part", p)
	}
}

func parseGeminiResponse(body []byte, sse bool) (*Response, error) {
	resp := &Response{Protocol: ProtocolGemini, Messages: []Message{}}
	msg := Message{Role: RoleAssistant, Blocks: []Block{}}
	seen := false
	visit := func(doc gjson.Result) {
		if r := doc.Get("response"); r.IsObject() {
			doc = r
		}
		if e := doc.Get("error"); e.Exists() {
			resp.Error = rawJSON(e)
			return
		}
		if v := doc.Get("responseId").String(); v != "" {
			resp.ID = v
		}
		if v := doc.Get("modelVersion").String(); v != "" {
			resp.Model = v
		}
		cand := doc.Get("candidates.0")
		if v := cand.Get("finishReason").String(); v != "" {
			resp.StopReason = v
			resp.Complete = true
		}
		each(cand.Get("content.parts"), func(_, p gjson.Result) bool {
			seen = true
			b := geminiPart(p)
			if (b.Type == BlockText || b.Type == BlockThinking) && b.Signature == "" {
				msg.Blocks = appendText(msg.Blocks, b.Type, b.Text)
			} else {
				msg.Blocks = append(msg.Blocks, b)
			}
			return true
		})
		if u := doc.Get("usageMetadata"); u.Exists() {
			if resp.Usage == nil {
				resp.Usage = &Usage{}
			}
			setUsage(&resp.Usage.Input, u.Get("promptTokenCount"))
			setUsage(&resp.Usage.Output, u.Get("candidatesTokenCount"))
			setUsage(&resp.Usage.CacheRead, u.Get("cachedContentTokenCount"))
			setUsage(&resp.Usage.Reasoning, u.Get("thoughtsTokenCount"))
		}
	}
	if sse {
		forEachJSONEvent(body, func(_ string, doc gjson.Result) { visit(doc) }, nil)
	} else {
		if !gjson.ValidBytes(body) {
			return nil, &ErrInvalidJSON{What: "gemini response"}
		}
		doc := gjson.ParseBytes(body)
		if doc.IsArray() {
			each(doc, func(_, v gjson.Result) bool { visit(v); return true })
		} else {
			visit(doc)
		}
	}
	if seen {
		resp.Messages = append(resp.Messages, msg)
	}
	return resp, nil
}

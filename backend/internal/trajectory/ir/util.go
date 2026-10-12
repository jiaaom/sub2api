package ir

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// forEachSSE 逐个事件回调 (event 名, data)。多行 data 以 \n 拼接。
func forEachSSE(body []byte, fn func(event string, data []byte)) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	var event string
	var data []byte
	flush := func() {
		if len(data) > 0 {
			fn(event, data)
		}
		event, data = "", nil
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
		} else if rest, ok := bytes.CutPrefix(line, []byte("event:")); ok {
			event = strings.TrimSpace(string(rest))
		}
	}
	flush()
}

// forEachJSONEvent SSE 中每个合法 JSON 的 data；[DONE] 通过 done 回调。
func forEachJSONEvent(body []byte, fn func(event string, doc gjson.Result), done func()) {
	forEachSSE(body, func(event string, data []byte) {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			if done != nil {
				done()
			}
			return
		}
		if gjson.ValidBytes(data) {
			fn(event, gjson.ParseBytes(data))
		}
	})
}

// toolArguments 工具参数：合法 JSON 存为 Arguments，否则存原文。空串视为 {}。
func toolArguments(raw string) (json.RawMessage, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return json.RawMessage(`{}`), ""
	}
	if gjson.Valid(s) {
		return json.RawMessage(s), ""
	}
	return nil, raw
}

// rawJSON 复制 gjson 值的原始 JSON。
func rawJSON(v gjson.Result) json.RawMessage {
	if !v.Exists() {
		return nil
	}
	return json.RawMessage(v.Raw)
}

func unknownBlock(kind string, v gjson.Result) Block {
	return Block{Type: BlockUnknown, Kind: kind, Raw: rawJSON(v)}
}

// imageBlock 内联 base64 只保留哈希与长度。
func imageBlock(mediaType, b64 string) Block {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		data = []byte(b64)
	}
	return Block{Type: BlockImage, MediaType: mediaType, Object: "sha256:" + sha256Hex(data), Bytes: len(data)}
}

// imageFromURL data: URL 转内联，否则保留 URL。
func imageFromURL(url string) Block {
	if rest, ok := strings.CutPrefix(url, "data:"); ok {
		if meta, b64, ok := strings.Cut(rest, ","); ok && strings.HasSuffix(meta, ";base64") {
			return imageBlock(strings.TrimSuffix(meta, ";base64"), b64)
		}
	}
	return Block{Type: BlockImage, Object: url}
}

// appendText 追加文本块；与上一个同类型（text / thinking）的块合并（流式增量拼接用）。
func appendText(blocks []Block, typ, text string) []Block {
	if text == "" {
		return blocks
	}
	if n := len(blocks); n > 0 && blocks[n-1].Type == typ {
		blocks[n-1].Text += text
		return blocks
	}
	return append(blocks, Block{Type: typ, Text: text})
}

// params 收集除 skip 之外的顶层字段。
func params(doc gjson.Result, skip ...string) map[string]json.RawMessage {
	skipSet := make(map[string]bool, len(skip))
	for _, k := range skip {
		skipSet[k] = true
	}
	out := map[string]json.RawMessage{}
	doc.ForEach(func(k, v gjson.Result) bool {
		if !skipSet[k.String()] {
			out[k.String()] = json.RawMessage(v.Raw)
		}
		return true
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

// contentBlocks 字符串内容或 [{type:text,text}] 数组转为块；非文本部分交给 other。
func contentBlocks(v gjson.Result, other func(part gjson.Result) Block) []Block {
	if v.Type == gjson.String {
		if v.String() == "" {
			return nil
		}
		return []Block{{Type: BlockText, Text: v.String()}}
	}
	if !v.IsArray() {
		return nil
	}
	var out []Block
	each(v, func(_, part gjson.Result) bool {
		if part.Type == gjson.String {
			out = append(out, Block{Type: BlockText, Text: part.String()})
			return true
		}
		out = append(out, other(part))
		return true
	})
	return out
}

func setUsage(dst *int64, v gjson.Result) {
	if v.Exists() && v.Int() > 0 {
		*dst = v.Int()
	}
}

// splitThink 把 content 开头的 <think>…</think> 拆成 thinking（部分开源模型的 CoT 写在正文里）。
func splitThink(text string) (thinking, rest string, ok bool) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "<think>") {
		return "", text, false
	}
	body := strings.TrimPrefix(trimmed, "<think>")
	end := strings.Index(body, "</think>")
	if end < 0 {
		return body, "", true // 未闭合：全部视为 thinking（流被截断）
	}
	return body[:end], strings.TrimLeft(body[end+len("</think>"):], "\r\n"), true
}

// liftThink 对文本块应用 splitThink（只处理消息的第一个块）。
func liftThink(blocks []Block) []Block {
	if len(blocks) == 0 || blocks[0].Type != BlockText {
		return blocks
	}
	thinking, rest, ok := splitThink(blocks[0].Text)
	if !ok {
		return blocks
	}
	out := []Block{{Type: BlockThinking, Text: thinking}}
	if rest != "" {
		out = append(out, Block{Type: BlockText, Text: rest})
	}
	return append(out, blocks[1:]...)
}

// each 只遍历数组（gjson 对标量和 null 也会回调一次）。
func each(v gjson.Result, fn func(_, item gjson.Result) bool) {
	if v.IsArray() {
		v.ForEach(fn)
	}
}

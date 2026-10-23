package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// stripRequestFieldsCredentialKey 按账号在出站前删除上游不认识的请求字段（本 fork 的改动，非上游功能）。
//
// 例：Infini-AI 的 Responses 端点用严格 JSON 解码，不认识 OpenAI 标准的 reasoning.summary，
// 整个请求回 400 `json: unknown field "summary"`；deepseek-harness 选了推理强度时会带上它，
// sub2api 自己把 Chat 的 reasoning_effort 转成 Responses 时也会补 summary="auto"。
// 在该账号的 credentials 里配置：
//
//	"strip_request_fields": ["reasoning.summary"]
//
// 路径为 gjson/sjson 语法；值也可以是逗号分隔的字符串。未配置的账号不受影响。
const stripRequestFieldsCredentialKey = "strip_request_fields"

// StripRequestFields 账号配置的出站前删除字段路径。
func (a *Account) StripRequestFields() []string {
	if a == nil || a.Credentials == nil {
		return nil
	}
	var paths []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			paths = append(paths, s)
		}
	}
	switch v := a.Credentials[stripRequestFieldsCredentialKey].(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	case []string:
		for _, s := range v {
			add(s)
		}
	case string:
		for _, s := range strings.Split(v, ",") {
			add(s)
		}
	}
	return paths
}

// stripAccountRequestFields 删除账号配置的字段；字段不存在或删除失败时原样保留。
func stripAccountRequestFields(account *Account, body []byte) []byte {
	for _, path := range account.StripRequestFields() {
		if !gjson.GetBytes(body, path).Exists() {
			continue
		}
		if stripped, err := sjson.DeleteBytes(body, path); err == nil {
			body = stripped
		}
	}
	return body
}

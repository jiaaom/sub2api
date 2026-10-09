package trajectory

import (
	"net/http"
	"net/url"
	"strings"
)

// 鉴权类 header 一律脱敏：上游账号的 OAuth token / API key 不能随录制文件外泄。
var sensitiveHeaders = map[string]struct{}{
	"authorization":        {},
	"proxy-authorization":  {},
	"x-api-key":            {},
	"api-key":              {},
	"x-goog-api-key":       {},
	"cookie":               {},
	"set-cookie":           {},
	"chatgpt-account-id":   {},
	"x-amz-security-token": {},
}

// 查询参数里的凭据（Gemini 的 ?key= 等）。
var sensitiveQueryParams = map[string]struct{}{
	"key":          {},
	"api_key":      {},
	"apikey":       {},
	"access_token": {},
	"token":        {},
}

func isSensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	if _, ok := sensitiveHeaders[lower]; ok {
		return true
	}
	return strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
		strings.HasSuffix(lower, "-key") || strings.Contains(lower, "api-key")
}

// redactHeaders 复制并脱敏 header；保留认证方案与末 4 位，便于区分是哪把凭据。
func redactHeaders(h http.Header) http.Header {
	if len(h) == 0 {
		return nil
	}
	out := make(http.Header, len(h))
	for name, values := range h {
		if !isSensitiveHeader(name) {
			out[name] = append([]string(nil), values...)
			continue
		}
		red := make([]string, len(values))
		whole := strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Set-Cookie")
		for i, v := range values {
			if whole {
				red[i] = "***" // cookie 可能含多个凭据，整体打码
				continue
			}
			red[i] = redactSecret(v)
		}
		out[name] = red
	}
	return out
}

func redactSecret(v string) string {
	v = strings.TrimSpace(v)
	prefix := ""
	if i := strings.IndexByte(v, ' '); i > 0 && i < 16 {
		prefix, v = v[:i+1], strings.TrimSpace(v[i+1:])
	}
	if len(v) <= 8 {
		return prefix + "***"
	}
	return prefix + "***" + v[len(v)-4:]
}

func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return ""
	}
	changed := false
	for k, vs := range values {
		if _, ok := sensitiveQueryParams[strings.ToLower(k)]; ok {
			for i := range vs {
				vs[i] = redactSecret(vs[i])
			}
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return values.Encode()
}

func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	cp := *u
	cp.User = nil
	cp.RawQuery = redactQuery(u.RawQuery)
	return cp.String()
}

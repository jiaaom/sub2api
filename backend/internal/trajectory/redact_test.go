package trajectory

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-abcdefghijklmnop")
	h.Set("X-Api-Key", "sk-proj-1234567890wxyz")
	h.Set("X-Goog-Api-Key", "AIzaSyABCDEFGH")
	h.Set("Cookie", "session=abc")
	h.Set("Chatgpt-Account-Id", "acct-12345678-9999")
	h.Set("X-Custom-Token", "tok_abcdefgh1234")
	h.Set("Anthropic-Version", "2023-06-01")
	h.Set("User-Agent", "claude-cli/2.1")
	h.Set("Content-Type", "application/json")

	got := redactHeaders(h)
	require.Equal(t, "Bearer ***mnop", got.Get("Authorization"))
	require.Equal(t, "***wxyz", got.Get("X-Api-Key"))
	require.Equal(t, "***EFGH", got.Get("X-Goog-Api-Key"))
	require.Equal(t, "***", got.Get("Cookie"))
	require.Equal(t, "***9999", got.Get("Chatgpt-Account-Id"))
	require.Equal(t, "***1234", got.Get("X-Custom-Token"))
	require.Equal(t, "2023-06-01", got.Get("Anthropic-Version"))
	require.Equal(t, "claude-cli/2.1", got.Get("User-Agent"))
	require.Equal(t, "application/json", got.Get("Content-Type"))
	require.Equal(t, "Bearer sk-ant-oat01-abcdefghijklmnop", h.Get("Authorization"), "original header must not be modified")
	require.Nil(t, redactHeaders(nil))
}

func TestRedactURLAndQuery(t *testing.T) {
	u, _ := url.Parse("https://user:pass@generativelanguage.googleapis.com/v1beta/models/gemini:streamGenerateContent?alt=sse&key=AIzaSySECRET9876")
	got := redactURL(u)
	require.NotContains(t, got, "SECRET")
	require.NotContains(t, got, "pass")
	require.Contains(t, got, "alt=sse")
	require.Contains(t, got, "%2A%2A%2A9876")
	require.Equal(t, "alt=sse", redactQuery("alt=sse"))
	require.Equal(t, "", redactQuery(""))
}

package service

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func stripFieldsTestAccount(strip any) *Account {
	creds := map[string]any{"api_key": "sk-test", "base_url": "https://cloud.example.com/maas"}
	if strip != nil {
		creds[stripRequestFieldsCredentialKey] = strip
	}
	return &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: creds, Status: StatusActive, Schedulable: true}
}

func TestStripRequestFieldsCredentialForms(t *testing.T) {
	require.Equal(t, []string{"reasoning.summary", "store"}, stripFieldsTestAccount([]any{" reasoning.summary ", "", "store", 3}).StripRequestFields())
	require.Equal(t, []string{"reasoning.summary", "store"}, stripFieldsTestAccount("reasoning.summary, store").StripRequestFields())
	require.Empty(t, stripFieldsTestAccount(nil).StripRequestFields())
	require.Empty(t, (*Account)(nil).StripRequestFields())
}

func TestStripAccountRequestFields(t *testing.T) {
	body := []byte(`{"model":"m","reasoning":{"effort":"low","summary":"auto"},"input":[]}`)

	out := stripAccountRequestFields(stripFieldsTestAccount([]any{"reasoning.summary", "missing.field"}), body)
	require.False(t, gjson.GetBytes(out, "reasoning.summary").Exists())
	require.Equal(t, "low", gjson.GetBytes(out, "reasoning.effort").String())

	require.Equal(t, body, stripAccountRequestFields(stripFieldsTestAccount(nil), body), "unconfigured accounts are untouched")
}

// 两个出站请求构造函数（所有转发路径都经过它们，含 Chat→Responses 转换）都要删掉配置的字段。
func TestBuildUpstreamRequestStripsConfiguredFields(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash-1017","stream":true,"reasoning":{"effort":"high","summary":"auto"},"input":[{"role":"user","content":"hi"}]}`)
	c := newDeepSeekChatFallbackContext(t, body)
	svc := &OpenAIGatewayService{cfg: deepSeekChatFallbackTestConfig()}
	account := stripFieldsTestAccount([]any{"reasoning.summary"})

	check := func(req *http.Request, err error) {
		t.Helper()
		require.NoError(t, err)
		sent, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(sent, "reasoning.summary").Exists(), string(sent))
		require.Equal(t, "high", gjson.GetBytes(sent, "reasoning.effort").String())
	}
	check(svc.buildUpstreamRequest(context.Background(), c, account, body, "sk-test", true, "", false))
	check(svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "sk-test"))
}

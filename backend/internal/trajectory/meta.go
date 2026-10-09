package trajectory

import (
	"net/http"
	"time"
)

// Exchange 状态。
const (
	StatusInProgress = "in_progress" // 仅出现在 spool 中
	StatusComplete   = "complete"
	StatusIncomplete = "incomplete" // 数据可能不完整：客户端断开、上游中断、进程崩溃后恢复、被强制落定
	StatusError      = "error"      // handler panic
)

// Attempt 结果。
const (
	OutcomeSuccess   = "success"    // 2xx 且响应读到 EOF
	OutcomeHTTPError = "http_error" // 非 2xx，响应读完
	OutcomeError     = "error"      // 传输层错误（Do 返回 error 或读取出错）
	OutcomeAborted   = "aborted"    // 响应未读完即被关闭
	OutcomeAbandoned = "abandoned"  // 一直未关闭，被强制落定
	OutcomePending   = "pending"    // 仅出现在 spool 中
)

// Meta 对应 raw/.../<exchange_id>/meta.json。字段含义见 design.md §2。
type Meta struct {
	FormatVersion int        `json:"format_version"`
	ExchangeID    string     `json:"exchange_id"`
	Status        string     `json:"status"`
	Recovered     bool       `json:"recovered,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`

	UserID     int64  `json:"user_id"`
	APIKeyID   int64  `json:"api_key_id"`
	APIKeyName string `json:"api_key_name,omitempty"`
	GroupID    int64  `json:"group_id"`
	GroupName  string `json:"group_name,omitempty"`

	Client       ClientMeta    `json:"client"`
	Attempts     []AttemptMeta `json:"attempts"`
	FinalAttempt *int          `json:"final_attempt,omitempty"`

	Model              ModelMeta `json:"model"`
	Usage              *Usage    `json:"usage,omitempty"`
	ResponseID         string    `json:"response_id,omitempty"`
	PreviousResponseID string    `json:"previous_response_id,omitempty"`
	StopReason         string    `json:"stop_reason,omitempty"`

	Truncated bool     `json:"truncated"`
	Notes     []string `json:"notes,omitempty"`
}

// ClientMeta 客户端侧（A/D）信息。
type ClientMeta struct {
	Method          string       `json:"method"`
	Path            string       `json:"path"`
	Query           string       `json:"query,omitempty"`
	Protocol        string       `json:"protocol"`
	Stream          bool         `json:"stream"`
	Headers         http.Header  `json:"headers,omitempty"`
	UserAgent       string       `json:"user_agent,omitempty"`
	ClientRequestID string       `json:"client_request_id,omitempty"`
	DetectedClient  string       `json:"detected_client"`
	SessionHint     *SessionHint `json:"session_hint,omitempty"`
	RequestFile     string       `json:"request_file,omitempty"`
	RequestBytes    int64        `json:"request_bytes"`
	ResponseFile    string       `json:"response_file,omitempty"`
	ResponseBytes   int64        `json:"response_bytes"`
	ResponseHeaders http.Header  `json:"response_headers,omitempty"`
	StatusCode      int          `json:"status_code,omitempty"`
	Canceled        bool         `json:"canceled,omitempty"`
}

// SessionHint 客户端提供的会话标识，只用于分组展示，不作为串联依据。
type SessionHint struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}

// AttemptMeta 一次上游尝试（B/C）。
type AttemptMeta struct {
	Index            int         `json:"index"`
	AccountID        int64       `json:"account_id"`
	Platform         string      `json:"platform,omitempty"`
	UpstreamProtocol string      `json:"upstream_protocol"`
	Method           string      `json:"method"`
	URL              string      `json:"url"`
	RequestHeaders   http.Header `json:"request_headers,omitempty"`
	RequestFile      string      `json:"request_file,omitempty"`
	BytesReq         int64       `json:"bytes_req"`
	StartedAt        time.Time   `json:"started_at"`
	StatusCode       int         `json:"status_code,omitempty"`
	ResponseHeaders  http.Header `json:"response_headers,omitempty"`
	ResponseFile     string      `json:"response_file,omitempty"`
	BytesResp        int64       `json:"bytes_resp"`
	TTFBMs           int64       `json:"ttfb_ms"`
	DurationMs       int64       `json:"duration_ms"`
	Outcome          string      `json:"outcome"`
	Error            string      `json:"error,omitempty"`
	Truncated        bool        `json:"truncated,omitempty"`
}

// ModelMeta 模型名：客户端请求的与上游实际返回的。
type ModelMeta struct {
	Requested string `json:"requested,omitempty"`
	Upstream  string `json:"upstream,omitempty"`
}

// Usage 按上游原始口径记录（Anthropic 的 input 不含缓存命中，OpenAI 的 input 含缓存命中）。
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Reasoning  int64 `json:"reasoning"`
}

// IndexEntry raw/<date>/index.ndjson 的一行。
type IndexEntry struct {
	ExchangeID       string     `json:"exchange_id"`
	Path             string     `json:"path"`
	Status           string     `json:"status"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	UserID           int64      `json:"user_id"`
	APIKeyID         int64      `json:"api_key_id"`
	GroupID          int64      `json:"group_id"`
	ClientPath       string     `json:"client_path"`
	ClientProtocol   string     `json:"client_protocol"`
	UpstreamProtocol string     `json:"upstream_protocol,omitempty"`
	DetectedClient   string     `json:"detected_client"`
	SessionHint      string     `json:"session_hint,omitempty"`
	Model            string     `json:"model,omitempty"`
	StatusCode       int        `json:"status_code,omitempty"`
	Attempts         int        `json:"attempts"`
	Usage            *Usage     `json:"usage,omitempty"`
	ResponseID       string     `json:"response_id,omitempty"`
}

func (m *Meta) indexEntry(relPath string) IndexEntry {
	e := IndexEntry{
		ExchangeID:     m.ExchangeID,
		Path:           relPath,
		Status:         m.Status,
		StartedAt:      m.StartedAt,
		FinishedAt:     m.FinishedAt,
		UserID:         m.UserID,
		APIKeyID:       m.APIKeyID,
		GroupID:        m.GroupID,
		ClientPath:     m.Client.Path,
		ClientProtocol: m.Client.Protocol,
		DetectedClient: m.Client.DetectedClient,
		StatusCode:     m.Client.StatusCode,
		Attempts:       len(m.Attempts),
		Usage:          m.Usage,
		ResponseID:     m.ResponseID,
	}
	e.Model = m.Model.Upstream
	if e.Model == "" {
		e.Model = m.Model.Requested
	}
	if m.Client.SessionHint != nil {
		e.SessionHint = m.Client.SessionHint.Value
	}
	if m.FinalAttempt != nil && *m.FinalAttempt < len(m.Attempts) {
		e.UpstreamProtocol = m.Attempts[*m.FinalAttempt].UpstreamProtocol
	} else if n := len(m.Attempts); n > 0 {
		e.UpstreamProtocol = m.Attempts[n-1].UpstreamProtocol
	}
	return e
}

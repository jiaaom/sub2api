package trajectory

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// 1.11 benchmark：录制关闭 / 开启时，经网关链转发一个流式响应的开销与首字节延迟。
//
//	go test -run '^$' -bench BenchmarkGatewayStream -benchmem ./internal/trajectory/
func BenchmarkGatewayStream(b *testing.B) {
	const chunks = 200
	chunk := "data: " + strings.Repeat("x", 250) + "\n\n"                                     // ~256B，接近真实 SSE delta 事件
	body := `{"model":"gpt-5.5","stream":true,"input":"` + strings.Repeat("y", 64<<10) + `"}` // 64KB 请求

	for _, mode := range []struct {
		name   string
		scopes []Scope
	}{
		{"recording_off", nil},
		{"recording_on", enabledScopes()},
	} {
		b.Run(mode.name, func(b *testing.B) {
			rec, err := New(Options{Root: b.TempDir()}, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer rec.Close()
			rec.SetScopes(mode.scopes)
			prev := Default()
			SetDefault(rec)
			defer SetDefault(prev)

			fake := &fakeUpstream{respond: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: io.NopCloser(&repeatReader{chunk: chunk, n: chunks})}, nil
			}}
			up := WrapHTTPUpstream(fake, rec)
			engine := newGatewayEngine(rec, up, testAPIKey(), proxyHandler(up, body, 1))

			var ttfb time.Duration
			b.ReportAllocs()
			b.SetBytes(int64(len(body) + chunks*len(chunk)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := &firstByteRecorder{ResponseRecorder: httptest.NewRecorder(), start: time.Now()}
				engine.ServeHTTP(w, req)
				ttfb += w.first
			}
			b.StopTimer()
			rec.Flush()
			b.ReportMetric(float64(ttfb.Nanoseconds())/float64(b.N), "ttfb-ns/op")
		})
	}
}

// repeatReader 每次 Read 返回一个 SSE 块，模拟逐事件到达的上游流。
type repeatReader struct {
	chunk string
	n     int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	r.n--
	return copy(p, r.chunk), nil
}

type firstByteRecorder struct {
	*httptest.ResponseRecorder
	start time.Time
	first time.Duration
}

func (w *firstByteRecorder) Write(p []byte) (int, error) {
	if w.first == 0 && len(p) > 0 {
		w.first = time.Since(w.start)
	}
	return w.ResponseRecorder.Write(p)
}

var _ gin.ResponseWriter = (*clientWriter)(nil)

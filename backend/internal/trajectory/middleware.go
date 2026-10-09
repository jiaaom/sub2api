package trajectory

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GinMiddleware 网关路由组的录制 middleware，必须挂在每条网关链的最前端（bodyLimit 之前），
// 这样记录到的 A 是客户端的原始 body（compositeTarget 等后续 middleware 可能会改写 body）。
// 是否录制要等 apiKeyAuth 之后才能确定，因此由 Exchange.Recording 延迟决定。
// 使用进程默认录制器（SetDefault）；未设置或全局开关关闭时只多一次原子读。
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		Default().handle(c, c.Next)
	}
}

// Around 把录制包在一个已有 handler 外层，用于无法插入独立 middleware 的路由链
// （例如 routes/gateway.go 的 rootRoute：把它的第一个 handler 包起来即可让录制位于链首）。
func Around(h gin.HandlerFunc) gin.HandlerFunc {
	if h == nil {
		return GinMiddleware()
	}
	return func(c *gin.Context) {
		Default().handle(c, func() { h(c) })
	}
}

func (r *Recorder) handle(c *gin.Context, next func()) {
	if !r.active() || c.Request == nil {
		next()
		return
	}
	skip := shouldSkip(c.Request)
	ex := r.newExchange(c, skip)
	c.Request = c.Request.WithContext(WithExchange(c.Request.Context(), ex))
	if !skip {
		ex.captureClientRequest(c.Request)
		c.Writer = &clientWriter{ResponseWriter: c.Writer, ex: ex}
	}
	finished := false
	defer func() { ex.finishFromGin(c, !finished) }()
	next()
	finished = true
}

// shouldSkip GET/HEAD/OPTIONS 与 WebSocket 升级请求不录（WS 由 forwarder hook 单独处理）。
// 仍然挂上 exchange 句柄，避免这些请求里的上游调用被误计为 orphan。
func shouldSkip(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
}

// clientWriter D：tee 回给客户端的字节。其余接口（Flush、Hijack、Status…）由内嵌的 ResponseWriter 提供。
type clientWriter struct {
	gin.ResponseWriter
	ex *Exchange
}

func (w *clientWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.ex.captureClientResponse(p[:n], w.Header())
	}
	return n, err
}

func (w *clientWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	if n > 0 {
		w.ex.captureClientResponse([]byte(s[:n]), w.Header())
	}
	return n, err
}

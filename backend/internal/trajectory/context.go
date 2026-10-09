package trajectory

import "context"

type exchangeContextKey struct{}

// WithExchange 把 exchange 句柄放进 request context，供 HTTPUpstream 装饰器取用。
func WithExchange(ctx context.Context, ex *Exchange) context.Context {
	if ex == nil {
		return ctx
	}
	return context.WithValue(ctx, exchangeContextKey{}, ex)
}

// FromContext 取出当前请求的 exchange 句柄；不存在时返回 nil。
// 注意：存在句柄不代表一定录制，是否录制由 Exchange.Recording 决定。
func FromContext(ctx context.Context) *Exchange {
	if ctx == nil {
		return nil
	}
	ex, _ := ctx.Value(exchangeContextKey{}).(*Exchange)
	return ex
}

package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/trajectory"
)

// ProvideHTTPUpstream 在 NewHTTPUpstream 外层套上 trajectory 录制装饰器（fork 专有，见 AGENTS.md Hook 清单）。
// rec 为 nil（录制目录不可用）时直接返回原始实现。
func ProvideHTTPUpstream(cfg *config.Config, rec *trajectory.Recorder) service.HTTPUpstream {
	return trajectory.WrapHTTPUpstream(NewHTTPUpstream(cfg), rec)
}

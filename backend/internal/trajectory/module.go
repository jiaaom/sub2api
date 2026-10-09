package trajectory

import (
	"log/slog"

	"github.com/google/wire"
)

// ProviderSet trajectory 模块的 wire 依赖（在 cmd/server/wire.go 注册）。
var ProviderSet = wire.NewSet(
	NewPostgresScopeRepository,
	wire.Bind(new(ScopeRepository), new(*PostgresScopeRepository)),
	ProvideRecorder,
)

// ProvideRecorder 创建并启动进程录制器，同时设为默认录制器（供网关 middleware 使用）。
// 存储目录不可用时返回 nil：录制整体关闭，网关照常工作。
func ProvideRecorder(repo ScopeRepository) *Recorder {
	opts := OptionsFromEnv()
	rec, err := New(opts, repo)
	if err != nil {
		slog.Error("trajectory: recorder disabled", "root", opts.Root, "error", err)
		SetDefault(nil)
		return nil
	}
	rec.Start()
	SetDefault(rec)
	slog.Info("trajectory: recorder ready", "root", rec.Root(), "global_enabled", rec.Stats().GlobalEnabled)
	return rec
}

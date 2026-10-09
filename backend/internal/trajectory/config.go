package trajectory

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvRoot 录制根目录；未设置时为 ${DATA_DIR:-./data}/trajectories。
	EnvRoot = "SUB2API_TRAJECTORY_ROOT"
	// EnvMaxBodyBytes 单个 body 落盘上限（字节），超出部分截断并标记 truncated。
	EnvMaxBodyBytes = "SUB2API_TRAJECTORY_MAX_BODY_BYTES"

	// FormatVersion 磁盘格式版本，写入 meta.json 与 FORMAT.md。
	FormatVersion = 1

	defaultMaxBodyBytes         int64 = 256 << 20
	defaultScopeRefreshInterval       = 15 * time.Second
	defaultProbeInterval              = time.Minute
	defaultAbandonAfter               = 30 * time.Minute
)

// Options 录制器运行参数。
type Options struct {
	Root                 string
	MaxBodyBytes         int64
	ScopeRefreshInterval time.Duration
	ProbeInterval        time.Duration
	// AbandonAfter middleware 结束后仍有未关闭的上游响应时，最长等待多久强制落定。
	AbandonAfter time.Duration
	Now          func() time.Time
}

// OptionsFromEnv 从环境变量读取配置（不改动 internal/config，避免与上游冲突）。
func OptionsFromEnv() Options {
	opts := Options{Root: strings.TrimSpace(os.Getenv(EnvRoot))}
	if opts.Root == "" {
		dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
		if dataDir == "" {
			dataDir = "./data"
		}
		opts.Root = filepath.Join(dataDir, "trajectories")
	}
	if raw := strings.TrimSpace(os.Getenv(EnvMaxBodyBytes)); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			opts.MaxBodyBytes = v
		}
	}
	return opts.withDefaults()
}

func (o Options) withDefaults() Options {
	if abs, err := filepath.Abs(o.Root); err == nil {
		o.Root = abs
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = defaultMaxBodyBytes
	}
	if o.ScopeRefreshInterval <= 0 {
		o.ScopeRefreshInterval = defaultScopeRefreshInterval
	}
	if o.ProbeInterval <= 0 {
		o.ProbeInterval = defaultProbeInterval
	}
	if o.AbandonAfter <= 0 {
		o.AbandonAfter = defaultAbandonAfter
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

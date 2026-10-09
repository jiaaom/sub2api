package trajectory

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// Recorder 录制器：持有开关快照、磁盘存储和运行计数。进程内单例（见 Default）。
type Recorder struct {
	opts  Options
	store *diskStore
	repo  ScopeRepository

	scopes atomic.Pointer[scopeSnapshot]

	degraded       atomic.Bool
	degradedReason atomic.Value // string

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
	pending  sync.WaitGroup // 异步落定中的 exchange

	stats recorderCounters
}

type recorderCounters struct {
	exchangesStarted   atomic.Int64
	exchangesFinalized atomic.Int64
	exchangesRecovered atomic.Int64
	attempts           atomic.Int64
	orphanCalls        atomic.Int64
	backgroundCalls    atomic.Int64
	lateAttempts       atomic.Int64
	writeErrors        atomic.Int64
}

// Stats 运行状态快照（供管理 API / 测试读取）。
type Stats struct {
	Root               string `json:"root"`
	FormatVersion      int    `json:"format_version"`
	GlobalEnabled      bool   `json:"global_enabled"`
	Degraded           bool   `json:"degraded"`
	DegradedReason     string `json:"degraded_reason,omitempty"`
	ExchangesStarted   int64  `json:"exchanges_started"`
	ExchangesFinalized int64  `json:"exchanges_finalized"`
	ExchangesRecovered int64  `json:"exchanges_recovered"`
	Attempts           int64  `json:"attempts"`
	// OrphanCalls 带网关请求标识（ClientRequestID）却找不到 exchange 句柄的上游调用——覆盖漏洞信号，应恒为 0。
	OrphanCalls int64 `json:"orphan_calls"`
	// BackgroundCalls 与任何网关请求无关的上游调用（额度探测、token 刷新等），仅供参考。
	BackgroundCalls int64 `json:"background_calls"`
	// LateAttempts exchange 已落定后才发生的上游调用（脱离请求生命周期的后台任务）。
	LateAttempts int64 `json:"late_attempts"`
	WriteErrors  int64 `json:"write_errors"`
}

var defaultRecorder atomic.Pointer[Recorder]

// SetDefault 设置进程默认录制器（GinMiddleware / Around 使用）。传 nil 可关闭。
func SetDefault(r *Recorder) { defaultRecorder.Store(r) }

// Default 返回进程默认录制器，可能为 nil。
func Default() *Recorder { return defaultRecorder.Load() }

// New 创建录制器并恢复 spool 中残留的 exchange。repo 可为 nil（开关全部视为关闭，测试可用 SetScopes）。
func New(opts Options, repo ScopeRepository) (*Recorder, error) {
	opts = opts.withDefaults()
	r := &Recorder{opts: opts, repo: repo, stop: make(chan struct{})}
	r.scopes.Store(newScopeSnapshot(nil))
	store, err := newDiskStore(opts.Root)
	if err != nil {
		return nil, err
	}
	r.store = store
	recovered, err := store.recoverSpool(opts.Now)
	r.stats.exchangesRecovered.Add(int64(recovered))
	if err != nil {
		slog.Error("trajectory: spool recovery failed", "error", err)
	}
	if recovered > 0 {
		slog.Warn("trajectory: recovered unfinished exchanges from spool", "count", recovered)
	}
	return r, nil
}

// Start 加载开关并启动后台刷新与降级探测循环。
func (r *Recorder) Start() {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := r.RefreshScopes(ctx); err != nil {
		slog.Warn("trajectory: initial scope load failed; recording stays off until next refresh", "error", err)
	}
	cancel()
	r.wg.Add(1)
	go r.loop()
}

// Close 停止后台循环并等待异步落定完成。
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	r.wg.Wait()
	r.pending.Wait()
}

// Flush 等待所有已结束的 exchange 完成异步落定（测试与关停使用）。
func (r *Recorder) Flush() { r.pending.Wait() }

func (r *Recorder) loop() {
	defer r.wg.Done()
	refresh := time.NewTicker(r.opts.ScopeRefreshInterval)
	probe := time.NewTicker(r.opts.ProbeInterval)
	defer refresh.Stop()
	defer probe.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-refresh.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := r.RefreshScopes(ctx); err != nil {
				slog.Warn("trajectory: scope refresh failed", "error", err)
			}
			cancel()
		case <-probe.C:
			if r.degraded.Load() {
				if err := r.store.probe(); err == nil {
					r.degraded.Store(false)
					slog.Info("trajectory: storage recovered, recording resumed")
				}
			}
		}
	}
}

// RefreshScopes 从仓库重新加载开关快照。
func (r *Recorder) RefreshScopes(ctx context.Context) error {
	if r.repo == nil {
		return nil
	}
	scopes, err := r.repo.ListScopes(ctx)
	if err != nil {
		return err
	}
	r.scopes.Store(newScopeSnapshot(scopes))
	return nil
}

// SetScopes 直接替换开关快照（测试与管理 API 写入后立即生效）。
func (r *Recorder) SetScopes(scopes []Scope) { r.scopes.Store(newScopeSnapshot(scopes)) }

// Root 录制根目录。
func (r *Recorder) Root() string { return r.opts.Root }

// active 全局开关开启且存储未降级。录制关闭时热路径只走到这里。
func (r *Recorder) active() bool {
	return r != nil && !r.degraded.Load() && r.scopes.Load().global
}

func (r *Recorder) decide(groupID, apiKeyID int64) bool {
	return !r.degraded.Load() && r.scopes.Load().decide(groupID, apiKeyID)
}

// fail 记录写入失败并进入降级：停止新的录制，网关请求不受影响。
func (r *Recorder) fail(op string, err error) {
	if err == nil {
		return
	}
	r.stats.writeErrors.Add(1)
	reason := fmt.Sprintf("%s: %v", op, err)
	r.degradedReason.Store(reason)
	if !r.degraded.Swap(true) {
		slog.Error("trajectory: storage failure, recording degraded", "op", op, "error", err)
	}
}

// Stats 返回运行状态。
func (r *Recorder) Stats() Stats {
	s := Stats{
		Root:               r.opts.Root,
		FormatVersion:      FormatVersion,
		GlobalEnabled:      r.scopes.Load().global,
		Degraded:           r.degraded.Load(),
		ExchangesStarted:   r.stats.exchangesStarted.Load(),
		ExchangesFinalized: r.stats.exchangesFinalized.Load(),
		ExchangesRecovered: r.stats.exchangesRecovered.Load(),
		Attempts:           r.stats.attempts.Load(),
		OrphanCalls:        r.stats.orphanCalls.Load(),
		BackgroundCalls:    r.stats.backgroundCalls.Load(),
		LateAttempts:       r.stats.lateAttempts.Load(),
		WriteErrors:        r.stats.writeErrors.Load(),
	}
	if reason, ok := r.degradedReason.Load().(string); ok && s.Degraded {
		s.DegradedReason = reason
	}
	return s
}

// noteUnattributed 统计找不到 exchange 句柄的上游调用。
func (r *Recorder) noteUnattributed(req *http.Request) {
	if !r.active() || req == nil {
		return
	}
	if id, _ := req.Context().Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(id) != "" {
		r.stats.orphanCalls.Add(1)
		slog.Warn("trajectory: upstream call inside a gateway request has no exchange (coverage gap)",
			"client_request_id", id, "host", req.URL.Host, "path", req.URL.Path)
		return
	}
	r.stats.backgroundCalls.Add(1)
}

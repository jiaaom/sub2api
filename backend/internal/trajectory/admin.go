package trajectory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// 管理 API：/api/v1/admin/recording/*（design.md §7）。
// 在 routes/admin.go 的管理员路由组上调用 RegisterAdminRoutes，鉴权与审计复用该组的 middleware。
// 处理函数在请求时才取 Default()，与 wire 的初始化顺序无关。

const (
	defaultExchangeLimit = 50
	maxExchangeLimit     = 500
	diskUsageTTL         = 30 * time.Second
)

// TargetLister 列出可配置录制开关的分组与 API key（toggle 页面用）。
type TargetLister interface {
	ListTargets(ctx context.Context) (*ScopeTargets, error)
}

// ScopeTargets 开关页面的数据：分组和 API key 的基本信息。
type ScopeTargets struct {
	Groups  []GroupTarget  `json:"groups"`
	APIKeys []APIKeyTarget `json:"api_keys"`
}

type GroupTarget struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Status   string `json:"status"`
}

type APIKeyTarget struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	UserID    int64  `json:"user_id"`
	UserEmail string `json:"user_email"`
	GroupID   *int64 `json:"group_id"`
	GroupName string `json:"group_name,omitempty"`
}

// ScopesView GET /scopes 的返回：开关配置 + 每个分组 / key 的生效结果。
type ScopesView struct {
	Global  bool          `json:"global"`
	Groups  []GroupScope  `json:"groups"`
	APIKeys []APIKeyScope `json:"api_keys"`
	Scopes  []Scope       `json:"scopes"`
	Stale   []Scope       `json:"stale,omitempty"` // 指向已删除分组 / key 的配置
}

type GroupScope struct {
	GroupTarget
	Enabled bool `json:"enabled"`
}

type APIKeyScope struct {
	APIKeyTarget
	// Override nil 表示继承分组。
	Override *bool `json:"override"`
	// Effective 全局开关打开时该 key 的请求是否会被录制（不含存储降级）。
	Effective bool `json:"effective"`
}

// ScopeUpdate PUT /scopes 的请求体。Enabled 为 null 时删除该配置（api_key 恢复继承；group 等价于关闭）。
type ScopeUpdate struct {
	Type    ScopeType `json:"type"`
	ID      int64     `json:"id"`
	Enabled *bool     `json:"enabled"`
}

// StatusView GET /status 的返回。
type StatusView struct {
	Stats
	Available    bool       `json:"available"`
	DiskBytes    int64      `json:"disk_bytes"`
	SpoolPending int        `json:"spool_pending"`
	Days         int        `json:"days"`
	Exchanges    int64      `json:"exchanges"`
	LastExchange *time.Time `json:"last_exchange_at,omitempty"`
	ScannedAt    time.Time  `json:"scanned_at"`
	// Index PG 索引状态；没有数据库时为 nil（sessions / 回放不可用）。
	Index *IndexStats `json:"index,omitempty"`
}

// ExchangeList GET /exchanges 的返回：从 index.ndjson 倒序读取的最近 exchange。
type ExchangeList struct {
	Items   []IndexEntry `json:"items"`
	HasMore bool         `json:"has_more"`
}

// RegisterAdminRoutes 在管理员路由组下挂载 /recording。
func RegisterAdminRoutes(admin *gin.RouterGroup) {
	g := admin.Group("/recording")
	g.GET("/status", adminStatus)
	g.GET("/scopes", adminListScopes)
	g.PUT("/scopes", adminUpdateScope)
	g.GET("/exchanges", adminListExchanges)
	g.GET("/exchanges/:id", adminGetExchange)
	g.GET("/exchanges/:id/raw", adminGetRaw)
	g.GET("/sessions", adminListSessions)
	g.GET("/sessions/:id", adminGetSession)
	g.GET("/sessions/:id/download", adminDownloadSession)
	g.PUT("/sessions/:id/label", adminSetSessionLabel)
	g.POST("/reindex", adminReindex)
}

func recorderOr503(c *gin.Context) *Recorder {
	rec := Default()
	if rec == nil {
		response.Error(c, http.StatusServiceUnavailable, "trajectory recorder is not available (storage directory unusable, see server log)")
	}
	return rec
}

func adminStatus(c *gin.Context) {
	rec := Default()
	if rec == nil {
		response.Success(c, StatusView{Available: false})
		return
	}
	response.Success(c, rec.statusView())
}

func adminListScopes(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	view, err := rec.scopesView(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, view)
}

func adminUpdateScope(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	var upd ScopeUpdate
	if err := c.ShouldBindJSON(&upd); err != nil {
		response.BadRequest(c, "invalid request body: "+err.Error())
		return
	}
	if err := validateScope(upd.Type, upd.ID); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := rec.updateScope(c.Request.Context(), upd); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	view, err := rec.scopesView(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, view)
}

func adminListExchanges(c *gin.Context) {
	rec := recorderOr503(c)
	if rec == nil {
		return
	}
	limit := defaultExchangeLimit
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		limit = min(v, maxExchangeLimit)
	}
	list, err := rec.recentExchanges(limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, list)
}

var errNoScopeRepository = errors.New("trajectory scope repository unavailable")

func (r *Recorder) updateScope(ctx context.Context, upd ScopeUpdate) error {
	if r.repo == nil {
		return errNoScopeRepository
	}
	var err error
	if upd.Enabled == nil {
		err = r.repo.DeleteScope(ctx, upd.Type, upd.ID)
	} else {
		err = r.repo.UpsertScope(ctx, Scope{Type: upd.Type, ID: upd.ID, Enabled: *upd.Enabled})
	}
	if err != nil {
		return err
	}
	// 立即刷新快照，不必等 15s 周期。
	return r.RefreshScopes(ctx)
}

func (r *Recorder) scopesView(ctx context.Context) (*ScopesView, error) {
	if r.repo == nil {
		return nil, errNoScopeRepository
	}
	scopes, err := r.repo.ListScopes(ctx)
	if err != nil {
		return nil, err
	}
	targets := &ScopeTargets{}
	if lister, ok := r.repo.(TargetLister); ok {
		if targets, err = lister.ListTargets(ctx); err != nil {
			return nil, err
		}
	}
	return buildScopesView(scopes, targets), nil
}

func buildScopesView(scopes []Scope, targets *ScopeTargets) *ScopesView {
	snap := newScopeSnapshot(scopes)
	groupSet := make(map[int64]bool, len(scopes))
	keySet := make(map[int64]*bool, len(scopes))
	view := &ScopesView{Global: snap.global, Scopes: scopes, Groups: []GroupScope{}, APIKeys: []APIKeyScope{}}
	if view.Scopes == nil {
		view.Scopes = []Scope{}
	}
	for _, sc := range scopes {
		switch sc.Type {
		case ScopeGroup:
			groupSet[sc.ID] = sc.Enabled
		case ScopeAPIKey:
			enabled := sc.Enabled
			keySet[sc.ID] = &enabled
		}
	}
	knownGroups := map[int64]bool{}
	for _, g := range targets.Groups {
		knownGroups[g.ID] = true
		view.Groups = append(view.Groups, GroupScope{GroupTarget: g, Enabled: groupSet[g.ID]})
	}
	knownKeys := map[int64]bool{}
	for _, k := range targets.APIKeys {
		knownKeys[k.ID] = true
		var gid int64
		if k.GroupID != nil {
			gid = *k.GroupID
		}
		effective := groupSet[gid]
		if o := keySet[k.ID]; o != nil {
			effective = *o
		}
		view.APIKeys = append(view.APIKeys, APIKeyScope{APIKeyTarget: k, Override: keySet[k.ID], Effective: effective})
	}
	for _, sc := range scopes {
		if (sc.Type == ScopeGroup && !knownGroups[sc.ID]) || (sc.Type == ScopeAPIKey && !knownKeys[sc.ID]) {
			view.Stale = append(view.Stale, sc)
		}
	}
	return view
}

// diskScan 根目录占用等统计（遍历目录较慢，缓存 diskUsageTTL）。
type diskScan struct {
	at           time.Time
	bytes        int64
	spoolPending int
	days         int
	exchanges    int64
	last         *time.Time
}

var diskScanCache struct {
	sync.Mutex
	root string
	scan diskScan
}

func (r *Recorder) statusView() StatusView {
	diskScanCache.Lock()
	defer diskScanCache.Unlock()
	now := r.opts.Now()
	if diskScanCache.root != r.opts.Root || now.Sub(diskScanCache.scan.at) > diskUsageTTL {
		diskScanCache.root = r.opts.Root
		diskScanCache.scan = scanRoot(r.opts.Root, now)
	}
	s := diskScanCache.scan
	var index *IndexStats
	if ix := r.indexer.Load(); ix != nil {
		st := ix.Stats()
		index = &st
	}
	return StatusView{
		Index:        index,
		Stats:        r.Stats(),
		Available:    true,
		DiskBytes:    s.bytes,
		SpoolPending: s.spoolPending,
		Days:         s.days,
		Exchanges:    s.exchanges,
		LastExchange: s.last,
		ScannedAt:    s.at,
	}
}

func scanRoot(root string, now time.Time) diskScan {
	s := diskScan{at: now}
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				s.bytes += info.Size()
			}
		}
		return nil
	})
	if entries, err := os.ReadDir(filepath.Join(root, dirSpool)); err == nil {
		s.spoolPending = len(entries)
	}
	days := indexDays(root)
	s.days = len(days)
	for _, day := range days {
		f, err := os.Open(filepath.Join(root, dirRaw, day, fileIndex))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) > 0 {
				s.exchanges++
			}
		}
		_ = f.Close()
	}
	if len(days) > 0 {
		if entries, _ := readIndexDesc(root, days[len(days)-1]); len(entries) > 0 {
			t := entries[0].StartedAt
			s.last = &t
		}
	}
	return s
}

// indexDays 返回 raw/ 下有 index.ndjson 的日期目录，升序。
func indexDays(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, dirRaw))
	if err != nil {
		return nil
	}
	var days []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := time.Parse("2006-01-02", e.Name()); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, dirRaw, e.Name(), fileIndex)); err == nil {
			days = append(days, e.Name())
		}
	}
	slices.Sort(days)
	return days
}

// readIndexDesc 读取某天的索引，按开始时间倒序。损坏的行跳过。
func readIndexDesc(root, day string) ([]IndexEntry, error) {
	data, err := os.ReadFile(filepath.Join(root, dirRaw, day, fileIndex))
	if err != nil {
		return nil, err
	}
	var out []IndexEntry
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e IndexEntry
		if json.Unmarshal(line, &e) == nil {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b IndexEntry) int { return b.StartedAt.Compare(a.StartedAt) })
	return out, nil
}

// recentExchanges 从最新日期往前读索引，最多 limit 条。
// 这是 PG 索引（Phase 3）之前的简易实现；数据量大时由 /sessions 取代。
func (r *Recorder) recentExchanges(limit int) (*ExchangeList, error) {
	days := indexDays(r.opts.Root)
	list := &ExchangeList{Items: []IndexEntry{}}
	for i := len(days) - 1; i >= 0; i-- {
		entries, err := readIndexDesc(r.opts.Root, days[i])
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if len(list.Items) == limit {
				list.HasMore = true
				return list, nil
			}
			list.Items = append(list.Items, e)
		}
	}
	return list, nil
}

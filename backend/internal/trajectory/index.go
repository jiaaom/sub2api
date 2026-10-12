package trajectory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/trajectory/ir"
)

// IndexRulesVersion 串联 / 分类规则版本。改动 index*.go 或 ir/canon.go 的判定逻辑时 +1，
// 启动后会自动清空索引并从 raw/ 全量重建。
const IndexRulesVersion = 1

const (
	EdgeRoot       = "root"
	EdgeContinue   = "continue"   // 请求以父节点的“请求+响应”为前缀
	EdgeBranch     = "branch"     // 从父节点链的中间分叉，或父节点已有后续（rewind / 编辑）
	EdgeRetry      = "retry"      // 与父节点请求完全相同
	EdgePointer    = "pointer"    // Responses previous_response_id 显式指针
	EdgeCompaction = "compaction" // 新根的开头包含父节点（压缩请求）的摘要文本

	KindMain              = "main"
	KindCompactionRequest = "compaction_request"
	KindCountTokens       = "count_tokens"

	indexQueueSize        = 4096
	compactionMinTextLen  = 200
	compactionHeadLen     = 256
	compactionLookback    = 30
	compactionNoHintRange = 6 * time.Hour
	titleMaxRunes         = 120
)

// ExchangeRow trajectory_exchanges 的一行。
type ExchangeRow struct {
	ExchangeID         string     `json:"exchange_id"`
	RawPath            string     `json:"raw_path"`
	StartedAt          time.Time  `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
	UserID             int64      `json:"user_id"`
	APIKeyID           int64      `json:"api_key_id"`
	GroupID            int64      `json:"group_id"`
	Status             string     `json:"status"`
	StatusCode         int        `json:"status_code"`
	ClientPath         string     `json:"client_path"`
	ClientProtocol     string     `json:"client_protocol"`
	UpstreamProtocol   string     `json:"upstream_protocol"`
	DetectedClient     string     `json:"detected_client"`
	Model              string     `json:"model"`
	SessionID          string     `json:"session_id"`
	SessionHint        string     `json:"session_hint"`
	InputTokens        int64      `json:"input_tokens"`
	OutputTokens       int64      `json:"output_tokens"`
	CacheReadTokens    int64      `json:"cache_read_tokens"`
	CacheWriteTokens   int64      `json:"cache_write_tokens"`
	ReasoningTokens    int64      `json:"reasoning_tokens"`
	ResponseID         string     `json:"response_id,omitempty"`
	PreviousResponseID string     `json:"previous_response_id,omitempty"`
	RootHash           string     `json:"root_hash,omitempty"`
	Req                [3]string  `json:"-"` // 按 ir.Levels 顺序
	Head               [3]string  `json:"-"`
	ReqMessages        int        `json:"req_messages"`
	RespMessages       int        `json:"resp_messages"`
	Tools              int        `json:"tools"`
	ParentID           *string    `json:"parent_id"`
	Edge               string     `json:"edge"`
	MatchLevel         string     `json:"match_level,omitempty"`
	PrefixMessages     int        `json:"prefix_messages"`
	Kind               string     `json:"kind"`
	RespTextHead       string     `json:"-"`
	RespTextLen        int        `json:"-"`
	Title              string     `json:"title,omitempty"`
	IndexError         string     `json:"index_error,omitempty"`

	// 仅用于建立 session 的提示来源，不入库。
	hintSource string
}

// SessionRow trajectory_sessions 的一行。
type SessionRow struct {
	SessionID       string    `json:"session_id"`
	UserID          int64     `json:"user_id"`
	HintSource      string    `json:"hint_source"`
	Hint            string    `json:"hint"`
	DetectedClient  string    `json:"detected_client"`
	FirstAt         time.Time `json:"first_at"`
	LastAt          time.Time `json:"last_at"`
	Exchanges       int       `json:"exchanges"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	CacheReadTokens int64     `json:"cache_read_tokens"`
	LastModel       string    `json:"last_model"`
	MaxMessages     int       `json:"max_messages"`
	Title           string    `json:"title"`
	// Label 用户自定义名称（trajectory_session_labels），为空表示未命名。
	Label string `json:"label"`
}

// linkCandidate 链尾哈希命中新请求链的已有 exchange。
type linkCandidate struct {
	ExchangeID string
	SessionID  string
	StartedAt  time.Time
	Req        string
	Head       string
}

// indexStore 索引的存储（Postgres 实现见 index_pg.go，测试用内存实现）。
type indexStore interface {
	insertExchange(ctx context.Context, row *ExchangeRow) (bool, error)
	upsertSession(ctx context.Context, row *ExchangeRow) error
	findByResponseID(ctx context.Context, userID int64, responseID string) (*ExchangeRow, error)
	findCandidates(ctx context.Context, userID int64, before time.Time, excludeID string, level int, hashes []string) ([]linkCandidate, error)
	countChildren(ctx context.Context, parentID, edge string) (int, error)
	compactionCandidates(ctx context.Context, userID int64, sessionID string, since, before time.Time, limit int) ([]ExchangeRow, error)
	setKind(ctx context.Context, exchangeID, kind string) error
	existingIDs(ctx context.Context, ids []string) (map[string]bool, error)
	truncate(ctx context.Context) error
	getState(ctx context.Context, key string) (string, error)
	setState(ctx context.Context, key, value string) error
}

// Indexer 把落盘的 exchange 解析成 IR、计算哈希链、串联并写入索引。
// 单 goroutine 顺序处理，保证父节点先于子节点入库（与落定顺序一致）。
type Indexer struct {
	store indexStore
	root  string

	queue   chan indexJob
	control chan func(context.Context)
	stop    chan struct{}
	done    chan struct{}
	pending inflight

	indexed atomic.Int64
	dropped atomic.Int64
	failed  atomic.Int64
	rebuild atomic.Bool
}

type indexJob struct {
	dir  string // 绝对路径
	meta *Meta
}

// IndexStats 索引运行状态。
type IndexStats struct {
	Indexed    int64 `json:"indexed"`
	Dropped    int64 `json:"dropped"`
	Failed     int64 `json:"failed"`
	Queue      int   `json:"queue"`
	Rebuilding bool  `json:"rebuilding"`
}

func newIndexer(store indexStore, root string) *Indexer {
	return &Indexer{
		store:   store,
		root:    root,
		queue:   make(chan indexJob, indexQueueSize),
		control: make(chan func(context.Context), 8),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Start 启动 worker：先检查规则版本（变化则全量重建，否则补齐缺失的 exchange），再处理队列。
func (ix *Indexer) Start() {
	ix.pending.Add(1)
	go ix.run()
}

func (ix *Indexer) run() {
	defer close(ix.done)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-ix.stop
		cancel()
	}()
	if err := ix.ensureCurrent(ctx); err != nil && ctx.Err() == nil {
		slog.Error("trajectory: index catch-up failed", "error", err)
	}
	ix.pending.Done()
	for {
		select {
		case <-ix.stop:
			return
		case fn := <-ix.control:
			fn(ctx)
			ix.pending.Done()
		case job := <-ix.queue:
			ix.process(ctx, job)
			ix.pending.Done()
		}
	}
}

// Close 停止 worker。队列中未处理的 exchange 下次启动时由 catch-up 补齐。
func (ix *Indexer) Close() {
	select {
	case <-ix.stop:
	default:
		close(ix.stop)
	}
	<-ix.done
}

// Flush 等待队列清空（测试用）。
func (ix *Indexer) Flush() { ix.pending.Wait() }

// Stats 返回运行状态。
func (ix *Indexer) Stats() IndexStats {
	return IndexStats{
		Indexed:    ix.indexed.Load(),
		Dropped:    ix.dropped.Load(),
		Failed:     ix.failed.Load(),
		Queue:      len(ix.queue),
		Rebuilding: ix.rebuild.Load(),
	}
}

// Enqueue 非阻塞入队；队列满时丢弃（下次启动或重建时补齐）。
func (ix *Indexer) Enqueue(dir string, meta *Meta) {
	ix.pending.Add(1)
	select {
	case ix.queue <- indexJob{dir: dir, meta: meta}:
	default:
		ix.pending.Done()
		ix.dropped.Add(1)
	}
}

// Rebuild 在 worker 中清空并全量重建索引（后台执行，立即返回）。
func (ix *Indexer) Rebuild() bool {
	if !ix.rebuild.CompareAndSwap(false, true) {
		return false
	}
	ix.pending.Add(1)
	ix.control <- func(ctx context.Context) {
		defer ix.rebuild.Store(false)
		if err := ix.rebuildAll(ctx); err != nil {
			slog.Error("trajectory: index rebuild failed", "error", err)
		}
	}
	return true
}

func (ix *Indexer) process(ctx context.Context, job indexJob) {
	if err := ix.indexOne(ctx, job.dir, job.meta); err != nil {
		ix.failed.Add(1)
		slog.Warn("trajectory: index exchange failed", "exchange_id", job.meta.ExchangeID, "error", err)
	}
}

const stateRulesVersion = "rules_version"

func (ix *Indexer) ensureCurrent(ctx context.Context) error {
	v, err := ix.store.getState(ctx, stateRulesVersion)
	if err != nil {
		return err
	}
	if v != fmt.Sprint(IndexRulesVersion) {
		slog.Info("trajectory: index rules changed, rebuilding", "from", v, "to", IndexRulesVersion)
		ix.rebuild.Store(true)
		defer ix.rebuild.Store(false)
		return ix.rebuildAll(ctx)
	}
	return ix.catchUp(ctx)
}

func (ix *Indexer) rebuildAll(ctx context.Context) error {
	if err := ix.store.truncate(ctx); err != nil {
		return err
	}
	if err := ix.catchUp(ctx); err != nil {
		return err
	}
	return ix.store.setState(ctx, stateRulesVersion, fmt.Sprint(IndexRulesVersion))
}

// catchUp 按日期顺序扫描 index.ndjson，补齐索引中缺失的 exchange（按开始时间顺序处理）。
func (ix *Indexer) catchUp(ctx context.Context) error {
	total := 0
	for _, day := range indexDays(ix.root) {
		entries, err := readIndexDesc(ix.root, day)
		if err != nil {
			return err
		}
		ids := make([]string, len(entries))
		for i, e := range entries {
			ids[i] = e.ExchangeID
		}
		have, err := ix.store.existingIDs(ctx, ids)
		if err != nil {
			return err
		}
		slices.Reverse(entries) // 升序
		for _, e := range entries {
			if have[e.ExchangeID] {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dir := filepath.Join(ix.root, filepath.FromSlash(e.Path))
			meta, err := loadMetaFile(dir)
			if err != nil {
				ix.failed.Add(1)
				slog.Warn("trajectory: index catch-up skipped exchange", "path", e.Path, "error", err)
				continue
			}
			if err := ix.indexOne(ctx, dir, meta); err != nil {
				ix.failed.Add(1)
				slog.Warn("trajectory: index exchange failed", "exchange_id", e.ExchangeID, "error", err)
				continue
			}
			total++
		}
	}
	if total > 0 {
		slog.Info("trajectory: indexed exchanges from disk", "count", total)
	}
	return nil
}

func loadMetaFile(dir string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(dir, fileMeta))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// parsedExchange 一个 exchange 解析后的 IR（最终 attempt 的 B / C）。
type parsedExchange struct {
	req    *ir.Request
	resp   *ir.Response
	att    *AttemptMeta
	reqErr error
}

// parseExchange 解析最终 attempt 的上游请求与响应。
func parseExchange(dir string, meta *Meta) parsedExchange {
	var p parsedExchange
	att := summaryAttempt(meta)
	if att == nil && len(meta.Attempts) > 0 {
		att = &meta.Attempts[len(meta.Attempts)-1]
	}
	p.att = att
	if att == nil || att.RequestFile == "" {
		return p
	}
	body, err := os.ReadFile(filepath.Join(dir, att.RequestFile))
	if err != nil {
		p.reqErr = err
		return p
	}
	p.req, p.reqErr = ir.ParseRequest(att.UpstreamProtocol, body)
	if att.ResponseFile != "" && att.StatusCode < 400 {
		if body, err := os.ReadFile(filepath.Join(dir, att.ResponseFile)); err == nil {
			p.resp, _ = ir.ParseResponse(att.UpstreamProtocol, body, strings.HasSuffix(att.ResponseFile, ".sse"))
		}
	}
	return p
}

func (ix *Indexer) indexOne(ctx context.Context, dir string, meta *Meta) error {
	rel, err := filepath.Rel(ix.root, dir)
	if err != nil {
		rel = dir
	}
	row := rowFromMeta(meta, filepath.ToSlash(rel))
	p := parseExchange(dir, meta)
	if p.att != nil {
		row.UpstreamProtocol = p.att.UpstreamProtocol
	}
	if p.reqErr != nil {
		row.IndexError = p.reqErr.Error()
	}
	if p.req != nil {
		if err := ix.link(ctx, row, p.req, p.resp); err != nil {
			return err
		}
	}
	if row.SessionID == "" {
		row.SessionID = "x" + ir.HashString(row.ExchangeID)
	}
	if strings.Contains(row.ClientPath, "count_tokens") {
		row.Kind = KindCountTokens
	}
	inserted, err := ix.store.insertExchange(ctx, row)
	if err != nil || !inserted {
		return err
	}
	ix.indexed.Add(1)
	if row.Edge == EdgeCompaction && row.ParentID != nil {
		if err := ix.store.setKind(ctx, *row.ParentID, KindCompactionRequest); err != nil {
			return err
		}
	}
	if row.Kind == KindCountTokens {
		return nil
	}
	return ix.store.upsertSession(ctx, row)
}

func rowFromMeta(m *Meta, rel string) *ExchangeRow {
	row := &ExchangeRow{
		ExchangeID:         m.ExchangeID,
		RawPath:            rel,
		StartedAt:          m.StartedAt,
		FinishedAt:         m.FinishedAt,
		UserID:             m.UserID,
		APIKeyID:           m.APIKeyID,
		GroupID:            m.GroupID,
		Status:             m.Status,
		StatusCode:         m.Client.StatusCode,
		ClientPath:         m.Client.Path,
		ClientProtocol:     m.Client.Protocol,
		DetectedClient:     m.Client.DetectedClient,
		Model:              firstNonEmpty(m.Model.Upstream, m.Model.Requested),
		ResponseID:         m.ResponseID,
		PreviousResponseID: m.PreviousResponseID,
		Edge:               EdgeRoot,
		Kind:               KindMain,
	}
	if m.Usage != nil {
		row.InputTokens, row.OutputTokens = m.Usage.Input, m.Usage.Output
		row.CacheReadTokens, row.CacheWriteTokens, row.ReasoningTokens = m.Usage.CacheRead, m.Usage.CacheWrite, m.Usage.Reasoning
	}
	if h := m.Client.SessionHint; h != nil && h.Value != "" {
		row.SessionHint = h.Value
		row.hintSource = h.Source
		row.SessionID = "h" + ir.HashString(fmt.Sprintf("%d\x00%s\x00%s", m.UserID, h.Source, h.Value))
	}
	return row
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// link 计算哈希链并找父节点（design.md §6.3–6.5、§6.1）。
func (ix *Indexer) link(ctx context.Context, row *ExchangeRow, req *ir.Request, resp *ir.Response) error {
	row.Tools = len(req.Tools)
	row.ReqMessages = len(req.Messages)
	var respMsgs []ir.Message
	if resp != nil {
		respMsgs = resp.Messages
		row.RespMessages = len(respMsgs)
		row.RespTextHead, row.RespTextLen = responseTextHead(resp)
	}
	row.RootHash = ir.Root(req)

	chains := make([]ir.Chain, len(ir.Levels))
	// 显式指针：B 只含新增 item，链从父节点的链尾继续。
	if req.PreviousResponseID != "" {
		parent, err := ix.store.findByResponseID(ctx, row.UserID, req.PreviousResponseID)
		if err != nil {
			return err
		}
		if parent != nil {
			row.ParentID = &parent.ExchangeID
			row.Edge, row.MatchLevel = EdgePointer, "pointer"
			row.RootHash = parent.RootHash
			if row.SessionID == "" {
				row.SessionID = parent.SessionID
			}
			for i, l := range ir.Levels {
				chains[i] = ir.Extend(ir.Chain{Hashes: []string{parent.Head[i]}}, req.Messages, 0, l)
			}
			ix.fillHashes(row, chains, respMsgs, len(req.Messages))
			return nil
		}
	}
	for i, l := range ir.Levels {
		chains[i] = ir.RequestChain(req, l)
	}
	ix.fillHashes(row, chains, respMsgs, len(req.Messages))

	if err := ix.linkByPrefix(ctx, row, chains); err != nil {
		return err
	}
	if row.ParentID == nil {
		if err := ix.linkCompaction(ctx, row, req); err != nil {
			return err
		}
		row.Title = requestTitle(req)
	}
	return nil
}

func (ix *Indexer) fillHashes(row *ExchangeRow, chains []ir.Chain, respMsgs []ir.Message, offset int) {
	for i, l := range ir.Levels {
		row.Req[i] = chains[i].Last()
		row.Head[i] = ir.Extend(chains[i], respMsgs, offset, l).Last()
	}
}

// linkByPrefix 在所有等级中找“链尾哈希出现在新请求链中且位置最靠后”的已有 exchange。
// 位置相同时优先更严格的等级、命中响应链尾（continue）而非请求链尾、开始时间更晚者。
func (ix *Indexer) linkByPrefix(ctx context.Context, row *ExchangeRow, chains []ir.Chain) error {
	type best struct {
		cand  linkCandidate
		level int
		pos   int
		head  bool
	}
	var b *best
	for li := range ir.Levels {
		chain := chains[li]
		cands, err := ix.store.findCandidates(ctx, row.UserID, row.StartedAt, row.ExchangeID, li, chain.Hashes)
		if err != nil {
			return err
		}
		pos := make(map[string]int, len(chain.Hashes))
		for i, h := range chain.Hashes {
			pos[h] = i
		}
		for _, c := range cands {
			for _, viaHead := range []bool{true, false} {
				h := c.Req
				if viaHead {
					h = c.Head
				}
				p, ok := pos[h]
				if !ok {
					continue
				}
				better := b == nil || p > b.pos ||
					(p == b.pos && li < b.level) ||
					(p == b.pos && li == b.level && viaHead && !b.head) ||
					(p == b.pos && li == b.level && viaHead == b.head && c.StartedAt.After(b.cand.StartedAt))
				if better {
					b = &best{cand: c, level: li, pos: p, head: viaHead}
				}
			}
		}
	}
	if b == nil {
		return nil
	}
	parentID := b.cand.ExchangeID
	row.ParentID = &parentID
	row.MatchLevel = ir.Levels[b.level].String()
	if b.pos > 0 {
		row.PrefixMessages = chains[b.level].MsgIndex[b.pos-1] + 1
	}
	last := len(chains[b.level].Hashes) - 1
	switch {
	case b.head:
		n, err := ix.store.countChildren(ctx, parentID, EdgeContinue)
		if err != nil {
			return err
		}
		row.Edge = EdgeContinue
		if n > 0 {
			row.Edge = EdgeBranch
		}
	case b.pos == last:
		row.Edge = EdgeRetry
	default:
		row.Edge = EdgeBranch
	}
	if row.SessionID == "" {
		row.SessionID = b.cand.SessionID
	}
	return nil
}

// linkCompaction 新根的开头几条消息若包含之前某个纯文本响应（摘要）的开头，则建立 compaction 边。
// 内容匹配而非时间猜测（design.md §6.1）；摘要较长，误配概率很低。
func (ix *Indexer) linkCompaction(ctx context.Context, row *ExchangeRow, req *ir.Request) error {
	text := normalizeSpace(leadingText(req, 4))
	if len(text) < compactionMinTextLen {
		return nil
	}
	sessionID := ""
	since := row.StartedAt.Add(-compactionNoHintRange)
	if row.SessionHint != "" {
		sessionID = row.SessionID
		since = time.Time{}
	}
	cands, err := ix.store.compactionCandidates(ctx, row.UserID, sessionID, since, row.StartedAt, compactionLookback)
	if err != nil {
		return err
	}
	for _, c := range cands {
		if c.RespTextLen >= compactionMinTextLen && c.RespTextHead != "" && strings.Contains(text, c.RespTextHead) {
			id := c.ExchangeID
			row.ParentID = &id
			row.Edge, row.MatchLevel = EdgeCompaction, "summary"
			if row.SessionID == "" {
				row.SessionID = c.SessionID
			}
			return nil
		}
	}
	return nil
}

// responseTextHead 纯文本（无 tool_call）响应的规范化开头与总长度。
func responseTextHead(resp *ir.Response) (string, int) {
	var parts []string
	for _, m := range resp.Messages {
		for _, b := range m.Blocks {
			switch b.Type {
			case ir.BlockToolCall:
				return "", 0
			case ir.BlockText:
				parts = append(parts, b.Text)
			}
		}
	}
	text := normalizeSpace(strings.Join(parts, " "))
	head := text
	if len(head) > compactionHeadLen {
		head = strings.ToValidUTF8(head[:compactionHeadLen], "")
	}
	return head, len(text)
}

// leadingText 前 n 条非 assistant 消息（以及 system）的文本。
func leadingText(req *ir.Request, n int) string {
	var parts []string
	write := func(blocks []ir.Block) {
		for _, b := range blocks {
			if b.Type == ir.BlockText {
				parts = append(parts, b.Text)
			}
		}
	}
	write(req.System)
	for i, m := range req.Messages {
		if i >= n {
			break
		}
		write(m.Blocks)
	}
	return strings.Join(parts, " ")
}

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// requestTitle 第一条用户消息中第一个“像人写的”文本块（跳过 <system-reminder> 之类的注入）。
func requestTitle(req *ir.Request) string {
	for _, m := range req.Messages {
		if m.Role != ir.RoleUser {
			continue
		}
		for _, b := range m.Blocks {
			t := strings.TrimSpace(b.Text)
			if b.Type != ir.BlockText || t == "" || strings.HasPrefix(t, "<") {
				continue
			}
			r := []rune(normalizeSpace(t))
			if len(r) > titleMaxRunes {
				return string(r[:titleMaxRunes]) + "…"
			}
			return string(r)
		}
	}
	return ""
}

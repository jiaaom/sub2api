package trajectory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// pgIndexStore 基于 migrations/901_trajectory_index.sql 的表。
type pgIndexStore struct {
	db *sql.DB
}

var reqColumns = [3]string{"req_exact", "req_nothink", "req_loose"}
var headColumns = [3]string{"head_exact", "head_nothink", "head_loose"}

const exchangeColumns = `exchange_id, raw_path, started_at, finished_at, user_id, api_key_id, group_id, status, status_code,
client_path, client_protocol, upstream_protocol, detected_client, model, session_id, session_hint,
input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
response_id, previous_response_id, root_hash, req_exact, req_nothink, req_loose, head_exact, head_nothink, head_loose,
req_messages, resp_messages, tools, parent_id, edge, match_level, prefix_messages, kind,
resp_text_head, resp_text_len, title, index_error`

func scanExchange(sc interface{ Scan(...any) error }) (*ExchangeRow, error) {
	var r ExchangeRow
	var finished sql.NullTime
	var parent sql.NullString
	err := sc.Scan(&r.ExchangeID, &r.RawPath, &r.StartedAt, &finished, &r.UserID, &r.APIKeyID, &r.GroupID, &r.Status, &r.StatusCode,
		&r.ClientPath, &r.ClientProtocol, &r.UpstreamProtocol, &r.DetectedClient, &r.Model, &r.SessionID, &r.SessionHint,
		&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.ReasoningTokens,
		&r.ResponseID, &r.PreviousResponseID, &r.RootHash, &r.Req[0], &r.Req[1], &r.Req[2], &r.Head[0], &r.Head[1], &r.Head[2],
		&r.ReqMessages, &r.RespMessages, &r.Tools, &parent, &r.Edge, &r.MatchLevel, &r.PrefixMessages, &r.Kind,
		&r.RespTextHead, &r.RespTextLen, &r.Title, &r.IndexError)
	if err != nil {
		return nil, err
	}
	if finished.Valid {
		t := finished.Time
		r.FinishedAt = &t
	}
	if parent.Valid {
		p := parent.String
		r.ParentID = &p
	}
	return &r, nil
}

func (s *pgIndexStore) insertExchange(ctx context.Context, r *ExchangeRow) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO trajectory_exchanges (`+exchangeColumns+`, rules_version)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43)
ON CONFLICT (exchange_id) DO NOTHING`,
		r.ExchangeID, r.RawPath, r.StartedAt, r.FinishedAt, r.UserID, r.APIKeyID, r.GroupID, r.Status, r.StatusCode,
		r.ClientPath, r.ClientProtocol, r.UpstreamProtocol, r.DetectedClient, r.Model, r.SessionID, r.SessionHint,
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.ReasoningTokens,
		r.ResponseID, r.PreviousResponseID, r.RootHash, r.Req[0], r.Req[1], r.Req[2], r.Head[0], r.Head[1], r.Head[2],
		r.ReqMessages, r.RespMessages, r.Tools, r.ParentID, r.Edge, r.MatchLevel, r.PrefixMessages, r.Kind,
		r.RespTextHead, r.RespTextLen, r.Title, r.IndexError, IndexRulesVersion)
	if err != nil {
		return false, fmt.Errorf("insert trajectory exchange: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *pgIndexStore) upsertSession(ctx context.Context, r *ExchangeRow) error {
	at := r.StartedAt
	if r.FinishedAt != nil {
		at = *r.FinishedAt
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO trajectory_sessions (session_id, user_id, hint_source, hint, detected_client, first_at, last_at, exchanges,
  input_tokens, output_tokens, cache_read_tokens, last_model, max_messages, title)
VALUES ($1,$2,$3,$4,$5,$6,$7,1,$8,$9,$10,$11,$12,$13)
ON CONFLICT (session_id) DO UPDATE SET
  first_at = LEAST(trajectory_sessions.first_at, EXCLUDED.first_at),
  last_at = GREATEST(trajectory_sessions.last_at, EXCLUDED.last_at),
  exchanges = trajectory_sessions.exchanges + 1,
  input_tokens = trajectory_sessions.input_tokens + EXCLUDED.input_tokens,
  output_tokens = trajectory_sessions.output_tokens + EXCLUDED.output_tokens,
  cache_read_tokens = trajectory_sessions.cache_read_tokens + EXCLUDED.cache_read_tokens,
  last_model = CASE WHEN EXCLUDED.last_at >= trajectory_sessions.last_at AND EXCLUDED.last_model <> '' THEN EXCLUDED.last_model ELSE trajectory_sessions.last_model END,
  max_messages = GREATEST(trajectory_sessions.max_messages, EXCLUDED.max_messages),
  detected_client = COALESCE(NULLIF(trajectory_sessions.detected_client, ''), EXCLUDED.detected_client),
  title = COALESCE(NULLIF(trajectory_sessions.title, ''), EXCLUDED.title)`,
		r.SessionID, r.UserID, r.hintSource, r.SessionHint, r.DetectedClient, r.StartedAt, at,
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.Model, r.ReqMessages+r.RespMessages, r.Title)
	if err != nil {
		return fmt.Errorf("upsert trajectory session: %w", err)
	}
	return nil
}

func (s *pgIndexStore) findByResponseID(ctx context.Context, userID int64, responseID string) (*ExchangeRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+exchangeColumns+` FROM trajectory_exchanges
WHERE user_id = $1 AND response_id = $2 ORDER BY started_at DESC LIMIT 1`, userID, responseID)
	r, err := scanExchange(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (s *pgIndexStore) findCandidates(ctx context.Context, userID int64, before time.Time, excludeID string, level int, hashes []string) ([]linkCandidate, error) {
	q := fmt.Sprintf(`SELECT exchange_id, session_id, started_at, %[1]s, %[2]s FROM trajectory_exchanges
WHERE user_id = $1 AND started_at <= $2 AND exchange_id <> $3 AND (%[1]s = ANY($4) OR %[2]s = ANY($4))`,
		reqColumns[level], headColumns[level])
	rows, err := s.db.QueryContext(ctx, q, userID, before, excludeID, pq.Array(hashes))
	if err != nil {
		return nil, fmt.Errorf("find trajectory link candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []linkCandidate
	for rows.Next() {
		var c linkCandidate
		if err := rows.Scan(&c.ExchangeID, &c.SessionID, &c.StartedAt, &c.Req, &c.Head); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *pgIndexStore) countChildren(ctx context.Context, parentID, edge string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trajectory_exchanges WHERE parent_id = $1 AND edge = $2`, parentID, edge).Scan(&n)
	return n, err
}

func (s *pgIndexStore) compactionCandidates(ctx context.Context, userID int64, sessionID string, since, before time.Time, limit int) ([]ExchangeRow, error) {
	args := []any{userID, before, since, compactionMinTextLen, limit}
	where := `user_id = $1 AND started_at < $2 AND started_at >= $3 AND resp_text_len >= $4`
	if sessionID != "" {
		where += ` AND session_id = $6`
		args = append(args, sessionID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+exchangeColumns+` FROM trajectory_exchanges WHERE `+where+` ORDER BY started_at DESC LIMIT $5`, args...)
	if err != nil {
		return nil, fmt.Errorf("find trajectory compaction candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ExchangeRow
	for rows.Next() {
		r, err := scanExchange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *pgIndexStore) setKind(ctx context.Context, exchangeID, kind string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE trajectory_exchanges SET kind = $2 WHERE exchange_id = $1`, exchangeID, kind)
	return err
}

func (s *pgIndexStore) startedBetween(ctx context.Context, userID int64, after, until time.Time, excludeID string) ([]ExchangeRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+exchangeColumns+` FROM trajectory_exchanges
WHERE user_id = $1 AND started_at > $2 AND started_at <= $3 AND exchange_id <> $4 ORDER BY started_at`, userID, after, until, excludeID)
	if err != nil {
		return nil, fmt.Errorf("find in-flight trajectory exchanges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ExchangeRow
	for rows.Next() {
		r, err := scanExchange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *pgIndexStore) latestMainBefore(ctx context.Context, sessionID string, before time.Time, excludeRoot string) (*ExchangeRow, error) {
	r, err := scanExchange(s.db.QueryRowContext(ctx, `SELECT `+exchangeColumns+` FROM trajectory_exchanges
WHERE session_id = $1 AND started_at < $2 AND kind = 'main' AND root_hash <> $3 AND root_hash <> ''
ORDER BY started_at DESC LIMIT 1`, sessionID, before, excludeRoot))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (s *pgIndexStore) updateLink(ctx context.Context, r *ExchangeRow) error {
	_, err := s.db.ExecContext(ctx, `UPDATE trajectory_exchanges SET parent_id = $2, edge = $3, match_level = $4, prefix_messages = $5
WHERE exchange_id = $1`, r.ExchangeID, r.ParentID, r.Edge, r.MatchLevel, r.PrefixMessages)
	return err
}

func (s *pgIndexStore) existingIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT exchange_id FROM trajectory_exchanges WHERE exchange_id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *pgIndexStore) truncate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `TRUNCATE trajectory_exchanges, trajectory_sessions`)
	return err
}

func (s *pgIndexStore) getState(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM trajectory_index_state WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *pgIndexStore) setState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO trajectory_index_state (key, value, updated_at) VALUES ($1, $2, NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, value)
	return err
}

// ---- 查询（管理 API 用） ----

// SessionQuery /sessions 的筛选条件。
type SessionQuery struct {
	UserID   int64
	Client   string
	Model    string
	Q        string
	From, To *time.Time
	Page     int
	PageSize int
}

func (s *pgIndexStore) listSessions(ctx context.Context, q SessionQuery) ([]SessionRow, int64, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if q.UserID > 0 {
		add("s.user_id = $%d", q.UserID)
	}
	if q.Client != "" {
		add("s.detected_client = $%d", q.Client)
	}
	if q.Model != "" {
		add("s.last_model ILIKE '%%' || $%d || '%%'", q.Model)
	}
	if q.Q != "" {
		add("(s.title ILIKE '%%' || $%[1]d || '%%' OR l.label ILIKE '%%' || $%[1]d || '%%' OR s.hint = $%[1]d OR s.session_id = $%[1]d)", q.Q)
	}
	if q.From != nil {
		add("s.last_at >= $%d", *q.From)
	}
	if q.To != nil {
		add("s.first_at < $%d", *q.To)
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trajectory_sessions s `+sessionLabelJoin+` `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.PageSize, (q.Page-1)*q.PageSize)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT `+sessionColumns+`
FROM trajectory_sessions s %s %s ORDER BY s.last_at DESC LIMIT $%d OFFSET $%d`, sessionLabelJoin, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []SessionRow{}
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

func (s *pgIndexStore) getSession(ctx context.Context, id string) (*SessionRow, []ExchangeRow, error) {
	r, err := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM trajectory_sessions s `+sessionLabelJoin+` WHERE s.session_id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+exchangeColumns+` FROM trajectory_exchanges WHERE session_id = $1 ORDER BY started_at`, id)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	ex := []ExchangeRow{}
	for rows.Next() {
		e, err := scanExchange(rows)
		if err != nil {
			return nil, nil, err
		}
		ex = append(ex, *e)
	}
	return r, ex, rows.Err()
}

const sessionColumns = `s.session_id, s.user_id, s.hint_source, s.hint, s.detected_client, s.first_at, s.last_at, s.exchanges,
  s.input_tokens, s.output_tokens, s.cache_read_tokens, s.last_model, s.max_messages, s.title, COALESCE(l.label, '')`

const sessionLabelJoin = `LEFT JOIN trajectory_session_labels l ON l.session_id = s.session_id`

func scanSession(sc interface{ Scan(...any) error }) (*SessionRow, error) {
	var r SessionRow
	err := sc.Scan(&r.SessionID, &r.UserID, &r.HintSource, &r.Hint, &r.DetectedClient, &r.FirstAt, &r.LastAt, &r.Exchanges,
		&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.LastModel, &r.MaxMessages, &r.Title, &r.Label)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// setSessionLabel 设置自定义名称；空串表示删除。不要求 session 已在索引中（重建期间也能改名）。
func (s *pgIndexStore) setSessionLabel(ctx context.Context, id, label string) error {
	if label == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM trajectory_session_labels WHERE session_id = $1`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO trajectory_session_labels (session_id, label, updated_at) VALUES ($1, $2, NOW())
ON CONFLICT (session_id) DO UPDATE SET label = EXCLUDED.label, updated_at = NOW()`, id, label)
	return err
}

func (s *pgIndexStore) getExchange(ctx context.Context, id string) (*ExchangeRow, error) {
	r, err := scanExchange(s.db.QueryRowContext(ctx, `SELECT `+exchangeColumns+` FROM trajectory_exchanges WHERE exchange_id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

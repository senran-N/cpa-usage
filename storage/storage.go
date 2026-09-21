package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Storage provides SQLite persistence and aggregation for usage records.
type Storage struct {
	db         *sql.DB
	queue      chan *Record
	stopWorker chan struct{}
	workerWg   sync.WaitGroup
	closed     bool
	closeLock  sync.Mutex
}

const (
	defaultQueueSize = 5000
	batchFlushSize   = 100
	flushInterval    = 250 * time.Millisecond
)

// Pagination and export bounds. These are exported so HTTP handlers can apply
// and report the exact same limits the storage layer enforces, instead of
// echoing back a page size that was silently clamped here.
const (
	// DefaultPageSize is used when a caller does not request a page size.
	DefaultPageSize = 20
	// MaxPageSize is the largest page the record query will ever return.
	MaxPageSize = 100
	// DefaultExportLimit is used when a caller does not request an export limit.
	DefaultExportLimit = 50000
	// MaxExportLimit bounds a single export so one request cannot pull an
	// unbounded number of rows into memory.
	MaxExportLimit = 200000
)

// ClampPageSize normalizes a requested page size into the supported range.
func ClampPageSize(pageSize int) int {
	if pageSize < 1 {
		return DefaultPageSize
	}
	if pageSize > MaxPageSize {
		return MaxPageSize
	}
	return pageSize
}

// ClampExportLimit normalizes a requested export row limit into the supported
// range.
func ClampExportLimit(limit int) int {
	if limit < 1 {
		return DefaultExportLimit
	}
	if limit > MaxExportLimit {
		return MaxExportLimit
	}
	return limit
}

// Open initializes or connects to the SQLite database at dbPath.
func Open(dbPath string) (*Storage, error) {
	// Enable WAL mode, busy timeout and normal synchronous mode via DSN
	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// SQLite WAL mode supports multiple readers and one writer concurrently
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	s := &Storage{
		db:         db,
		queue:      make(chan *Record, defaultQueueSize),
		stopWorker: make(chan struct{}),
	}

	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	s.workerWg.Add(1)
	go s.flushWorker()

	return s, nil
}

func (s *Storage) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS usage_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider TEXT NOT NULL,
		base_url TEXT DEFAULT '',
		executor_type TEXT DEFAULT '',
		model TEXT NOT NULL,
		alias TEXT DEFAULT '',
		api_key TEXT DEFAULT '',
		session_id TEXT DEFAULT '',
		parent_session_id TEXT DEFAULT '',
		auth_id TEXT DEFAULT '',
		auth_index TEXT DEFAULT '',
		auth_type TEXT DEFAULT '',
		source TEXT DEFAULT '',
		reasoning_effort TEXT DEFAULT '',
		service_tier TEXT DEFAULT '',
		generate INTEGER DEFAULT 1,
		requested_at TEXT NOT NULL,
		requested_at_unix INTEGER NOT NULL,
		latency_ms INTEGER DEFAULT 0,
		ttft_ms INTEGER DEFAULT 0,
		failed INTEGER DEFAULT 0,
		failure_status_code INTEGER DEFAULT 0,
		failure_body TEXT DEFAULT '',
		fail_summary TEXT DEFAULT '',
		header_error_kind TEXT DEFAULT '',
		header_error_code TEXT DEFAULT '',
		header_trace_id TEXT DEFAULT '',
		header_quota_recover_at_ms INTEGER DEFAULT 0,
		header_quota_used_percent REAL DEFAULT NULL,
		header_quota_window_minutes REAL DEFAULT NULL,
		header_secondary_quota_recover_at_ms INTEGER DEFAULT 0,
		header_secondary_quota_used_percent REAL DEFAULT NULL,
		header_secondary_quota_window_minutes REAL DEFAULT NULL,
		rate_limit_reached_type TEXT DEFAULT '',
		plan_type TEXT DEFAULT '',
		response_model TEXT DEFAULT '',
		model_mismatch INTEGER DEFAULT 0,
		input_tokens INTEGER DEFAULT 0,
		output_tokens INTEGER DEFAULT 0,
		reasoning_tokens INTEGER DEFAULT 0,
		cached_tokens INTEGER DEFAULT 0,
		cache_read_tokens INTEGER DEFAULT 0,
		cache_creation_tokens INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		input_cost REAL DEFAULT 0.0,
		output_cost REAL DEFAULT 0.0,
		cache_read_cost REAL DEFAULT 0.0,
		cache_creation_cost REAL DEFAULT 0.0,
		total_cost REAL DEFAULT 0.0,
		matched_model TEXT DEFAULT ''
	);

	CREATE INDEX IF NOT EXISTS idx_usage_requested_at_unix ON usage_records(requested_at_unix);
	CREATE INDEX IF NOT EXISTS idx_usage_model ON usage_records(model);
	CREATE INDEX IF NOT EXISTS idx_usage_api_key ON usage_records(api_key);
	CREATE INDEX IF NOT EXISTS idx_usage_auth_id ON usage_records(auth_id);
	CREATE INDEX IF NOT EXISTS idx_usage_provider ON usage_records(provider);
	CREATE INDEX IF NOT EXISTS idx_usage_failed ON usage_records(failed);

	CREATE TABLE IF NOT EXISTS custom_prices (
		model TEXT PRIMARY KEY,
		input_cost_per_token REAL DEFAULT 0,
		output_cost_per_token REAL DEFAULT 0,
		cache_read_cost_per_token REAL DEFAULT 0,
		cache_creation_cost_per_token REAL DEFAULT 0,
		cache_creation_input_token_cost_above_1hr REAL DEFAULT 0,
		supports_prompt_caching INTEGER DEFAULT 0,
		source TEXT DEFAULT 'manual',
		updated_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS synced_prices (
		model TEXT PRIMARY KEY,
		input_cost_per_token REAL DEFAULT 0,
		output_cost_per_token REAL DEFAULT 0,
		cache_read_cost_per_token REAL DEFAULT 0,
		cache_creation_cost_per_token REAL DEFAULT 0,
		cache_creation_input_token_cost_above_1hr REAL DEFAULT 0,
		supports_prompt_caching INTEGER DEFAULT 0,
		source TEXT DEFAULT '',
		updated_at INTEGER NOT NULL
	);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	return s.migrateSchema()
}

func (s *Storage) migrateSchema() error {
	rows, err := s.db.Query("PRAGMA table_info(usage_records)")
	if err != nil {
		return err
	}
	defer rows.Close()

	existingCols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err == nil {
			existingCols[strings.ToLower(name)] = true
		}
	}

	newColumns := []struct {
		name string
		stmt string
	}{
		{"fail_summary", "ALTER TABLE usage_records ADD COLUMN fail_summary TEXT DEFAULT ''"},
		{"header_error_kind", "ALTER TABLE usage_records ADD COLUMN header_error_kind TEXT DEFAULT ''"},
		{"header_error_code", "ALTER TABLE usage_records ADD COLUMN header_error_code TEXT DEFAULT ''"},
		{"header_trace_id", "ALTER TABLE usage_records ADD COLUMN header_trace_id TEXT DEFAULT ''"},
		{"header_quota_recover_at_ms", "ALTER TABLE usage_records ADD COLUMN header_quota_recover_at_ms INTEGER DEFAULT 0"},
		{"header_quota_used_percent", "ALTER TABLE usage_records ADD COLUMN header_quota_used_percent REAL DEFAULT NULL"},
		{"header_quota_window_minutes", "ALTER TABLE usage_records ADD COLUMN header_quota_window_minutes REAL DEFAULT NULL"},
		{"header_secondary_quota_recover_at_ms", "ALTER TABLE usage_records ADD COLUMN header_secondary_quota_recover_at_ms INTEGER DEFAULT 0"},
		{"header_secondary_quota_used_percent", "ALTER TABLE usage_records ADD COLUMN header_secondary_quota_used_percent REAL DEFAULT NULL"},
		{"header_secondary_quota_window_minutes", "ALTER TABLE usage_records ADD COLUMN header_secondary_quota_window_minutes REAL DEFAULT NULL"},
		{"rate_limit_reached_type", "ALTER TABLE usage_records ADD COLUMN rate_limit_reached_type TEXT DEFAULT ''"},
		{"plan_type", "ALTER TABLE usage_records ADD COLUMN plan_type TEXT DEFAULT ''"},
		{"response_model", "ALTER TABLE usage_records ADD COLUMN response_model TEXT DEFAULT ''"},
		{"model_mismatch", "ALTER TABLE usage_records ADD COLUMN model_mismatch INTEGER DEFAULT 0"},
	}

	for _, col := range newColumns {
		if !existingCols[col.name] {
			if _, err := s.db.Exec(col.stmt); err != nil {
				return fmt.Errorf("failed to add column %s: %w", col.name, err)
			}
		}
	}
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_usage_model_mismatch ON usage_records(model_mismatch)")
	return nil
}

// Ingest asynchronously enqueues a record for batch writing.
// It will not block the caller unless the internal queue is completely full.
func (s *Storage) Ingest(r *Record) {
	if r == nil {
		return
	}
	s.closeLock.Lock()
	defer s.closeLock.Unlock()
	if s.closed {
		return
	}

	select {
	case s.queue <- r:
	default:
		// Queue full, insert directly in a goroutine to avoid dropping data or blocking caller
		go func(rec *Record) {
			_ = s.InsertRecord(rec)
		}(r)
	}
}

func (s *Storage) flushWorker() {
	defer s.workerWg.Done()

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	batch := make([]*Record, 0, batchFlushSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = s.BatchInsertRecords(batch)
		batch = make([]*Record, 0, batchFlushSize)
	}

	for {
		select {
		case <-s.stopWorker:
			// Drain remaining records in queue
			for {
				select {
				case r := <-s.queue:
					batch = append(batch, r)
					if len(batch) >= batchFlushSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case r := <-s.queue:
			batch = append(batch, r)
			if len(batch) >= batchFlushSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Close drains queued records and closes the underlying database.
func (s *Storage) Close() error {
	s.closeLock.Lock()
	if s.closed {
		s.closeLock.Unlock()
		return nil
	}
	s.closed = true
	s.closeLock.Unlock()

	close(s.stopWorker)
	s.workerWg.Wait()

	return s.db.Close()
}

// InsertRecord synchronously writes a single record into the database.
func (s *Storage) InsertRecord(r *Record) error {
	return s.BatchInsertRecords([]*Record{r})
}

// BatchInsertRecords writes a batch of records inside a single transaction.
func (s *Storage) BatchInsertRecords(records []*Record) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.Prepare(`
		INSERT INTO usage_records (
			provider, base_url, executor_type, model, alias,
			api_key, session_id, parent_session_id, auth_id, auth_index,
			auth_type, source, reasoning_effort, service_tier, generate,
			requested_at, requested_at_unix, latency_ms, ttft_ms, failed,
			failure_status_code, failure_body, fail_summary, header_error_kind, header_error_code,
			header_trace_id, header_quota_recover_at_ms, header_quota_used_percent, header_quota_window_minutes,
			header_secondary_quota_recover_at_ms, header_secondary_quota_used_percent, header_secondary_quota_window_minutes,
			rate_limit_reached_type, plan_type,
			response_model, model_mismatch,
			input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
			input_cost, output_cost, cache_read_cost, cache_creation_cost, total_cost,
			matched_model
		) VALUES (
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare stmt: %w", err)
	}
	defer stmt.Close()

	for _, r := range records {
		reqAt := r.RequestedAt
		if reqAt.IsZero() {
			reqAt = time.Now()
		}
		reqAtUTC := reqAt.UTC()
		genInt := 0
		if r.Generate {
			genInt = 1
		}
		failedInt := 0
		if r.Failed {
			failedInt = 1
		}
		mismatchInt := 0
		if r.ModelMismatch {
			mismatchInt = 1
		}

		res, err := stmt.Exec(
			r.Provider, r.BaseURL, r.ExecutorType, r.Model, r.Alias,
			r.APIKey, r.SessionID, r.ParentSessionID, r.AuthID, r.AuthIndex,
			r.AuthType, r.Source, r.ReasoningEffort, r.ServiceTier, genInt,
			reqAtUTC.Format("2006-01-02 15:04:05"), reqAtUTC.Unix(), r.LatencyMs, r.TTFTMs, failedInt,
			r.FailureStatusCode, r.FailureBody, r.FailSummary, r.HeaderErrorKind, r.HeaderErrorCode,
			r.HeaderTraceID, r.HeaderQuotaRecoverAtMS, r.HeaderQuotaUsedPercent, r.HeaderQuotaWindowMinutes,
			r.HeaderSecondaryQuotaRecoverAtMS, r.HeaderSecondaryQuotaUsedPercent, r.HeaderSecondaryQuotaWindowMinutes,
			r.RateLimitReachedType, r.PlanType,
			r.ResponseModel, mismatchInt,
			r.InputTokens, r.OutputTokens, r.ReasoningTokens,
			r.CachedTokens, r.CacheReadTokens, r.CacheCreationTokens, r.TotalTokens,
			r.InputCost, r.OutputCost, r.CacheReadCost, r.CacheCreationCost, r.TotalCost,
			r.MatchedModel,
		)
		if err != nil {
			return fmt.Errorf("failed to exec insert: %w", err)
		}
		if r.ID == 0 {
			if id, err := res.LastInsertId(); err == nil {
				r.ID = id
			}
		}
	}

	return tx.Commit()
}

func buildWhereClause(filter QueryFilter) (string, []interface{}) {
	var where []string
	var args []interface{}

	if filter.StartTime != nil && !filter.StartTime.IsZero() {
		where = append(where, "requested_at_unix >= ?")
		args = append(args, filter.StartTime.UTC().Unix())
	}
	if filter.EndTime != nil && !filter.EndTime.IsZero() {
		where = append(where, "requested_at_unix <= ?")
		args = append(args, filter.EndTime.UTC().Unix())
	}
	if filter.APIKey != "" {
		where = append(where, "api_key = ?")
		args = append(args, filter.APIKey)
	}
	if filter.Model != "" {
		where = append(where, "model = ?")
		args = append(args, filter.Model)
	}
	if filter.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, filter.Provider)
	}
	if filter.AuthID != "" {
		where = append(where, "auth_id = ?")
		args = append(args, filter.AuthID)
	}
	if filter.Failed != nil {
		failedInt := 0
		if *filter.Failed {
			failedInt = 1
		}
		where = append(where, "failed = ?")
		args = append(args, failedInt)
	}
	if filter.ModelMismatch != nil {
		mismatchInt := 0
		if *filter.ModelMismatch {
			mismatchInt = 1
		}
		where = append(where, "model_mismatch = ?")
		args = append(args, mismatchInt)
	}
	if filter.Search != "" {
		pattern := "%" + filter.Search + "%"
		where = append(where, "(model LIKE ? OR api_key LIKE ? OR auth_id LIKE ? OR provider LIKE ?)")
		args = append(args, pattern, pattern, pattern, pattern)
	}

	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// GetSummary returns overall summary statistics matching the filter.
func (s *Storage) GetSummary(filter QueryFilter) (*SummaryStats, error) {
	where, args := buildWhereClause(filter)
	query := `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(total_cost), 0.0),
			COALESCE(AVG(latency_ms), 0.0)
		FROM usage_records` + where

	row := s.db.QueryRow(query, args...)
	stats := &SummaryStats{}
	err := row.Scan(
		&stats.TotalRequests,
		&stats.SuccessRequests,
		&stats.FailedRequests,
		&stats.TotalTokens,
		&stats.InputTokens,
		&stats.OutputTokens,
		&stats.ReasoningTokens,
		&stats.CacheReadTokens,
		&stats.CacheCreationTokens,
		&stats.TotalCost,
		&stats.AvgLatencyMs,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query summary: %w", err)
	}
	return stats, nil
}

// GetTimeSeries returns time-bucketed metrics for trends and graphs.
// interval can be "hour", "day", or "minute" (defaults to "hour").
func (s *Storage) GetTimeSeries(filter QueryFilter, interval string) ([]*TimeSeriesPoint, error) {
	where, args := buildWhereClause(filter)

	format := "%Y-%m-%d %H:00"
	switch interval {
	case "day":
		format = "%Y-%m-%d"
	case "minute":
		format = "%Y-%m-%d %H:%M"
	}

	query := fmt.Sprintf(`
		SELECT
			strftime('%s', requested_at) AS bucket,
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(total_cost), 0.0),
			COALESCE(AVG(latency_ms), 0.0)
		FROM usage_records
		%s
		GROUP BY bucket
		ORDER BY bucket ASC
	`, format, where)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query time series: %w", err)
	}
	defer rows.Close()

	var points []*TimeSeriesPoint
	for rows.Next() {
		p := &TimeSeriesPoint{}
		if err := rows.Scan(
			&p.Timestamp,
			&p.Requests,
			&p.SuccessCount,
			&p.FailedCount,
			&p.TotalTokens,
			&p.TotalCost,
			&p.AvgLatencyMs,
		); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// GetModelStats returns usage aggregated by model name.
func (s *Storage) GetModelStats(filter QueryFilter) ([]*ModelStat, error) {
	where, args := buildWhereClause(filter)
	query := `
		SELECT
			model,
			COUNT(*),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(total_cost), 0.0),
			COALESCE(AVG(latency_ms), 0.0)
		FROM usage_records
		` + where + `
		GROUP BY model
		ORDER BY total_cost DESC, COUNT(*) DESC
	`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query model stats: %w", err)
	}
	defer rows.Close()

	var stats []*ModelStat
	for rows.Next() {
		m := &ModelStat{}
		if err := rows.Scan(
			&m.Model,
			&m.Requests,
			&m.TotalTokens,
			&m.InputTokens,
			&m.OutputTokens,
			&m.TotalCost,
			&m.AvgLatencyMs,
		); err != nil {
			return nil, err
		}
		stats = append(stats, m)
	}
	return stats, rows.Err()
}

// GetAPIKeyStats returns usage aggregated by API key.
func (s *Storage) GetAPIKeyStats(filter QueryFilter) ([]*APIKeyStat, error) {
	whereClause, args := buildWhereClause(filter)
	if whereClause == "" {
		whereClause = " WHERE api_key != '' "
	} else {
		whereClause += " AND api_key != '' "
	}

	query := `
		SELECT
			api_key,
			COUNT(*),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(total_cost), 0.0),
			MAX(requested_at)
		FROM usage_records
		` + whereClause + `
		GROUP BY api_key
		ORDER BY total_cost DESC, COUNT(*) DESC
	`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query api key stats: %w", err)
	}
	defer rows.Close()

	var stats []*APIKeyStat
	for rows.Next() {
		k := &APIKeyStat{}
		var lastUsedStr string
		if err := rows.Scan(
			&k.APIKey,
			&k.Requests,
			&k.TotalTokens,
			&k.TotalCost,
			&lastUsedStr,
		); err != nil {
			return nil, err
		}
		if t, err := time.Parse("2006-01-02 15:04:05", lastUsedStr); err == nil {
			k.LastUsedAt = t
		}
		stats = append(stats, k)
	}
	return stats, rows.Err()
}

// GetAuthStats returns usage aggregated by auth credential.
func (s *Storage) GetAuthStats(filter QueryFilter) ([]*AuthStat, error) {
	whereClause, args := buildWhereClause(filter)
	if whereClause == "" {
		whereClause = " WHERE auth_id != '' "
	} else {
		whereClause += " AND auth_id != '' "
	}

	query := `
		SELECT
			auth_id,
			auth_type,
			provider,
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(total_cost), 0.0),
			COALESCE(AVG(latency_ms), 0.0)
		FROM usage_records
		` + whereClause + `
		GROUP BY auth_id, auth_type, provider
		ORDER BY COUNT(*) DESC
	`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query auth stats: %w", err)
	}
	defer rows.Close()

	var stats []*AuthStat
	for rows.Next() {
		a := &AuthStat{}
		if err := rows.Scan(
			&a.AuthID,
			&a.AuthType,
			&a.Provider,
			&a.Requests,
			&a.FailedCount,
			&a.TotalTokens,
			&a.TotalCost,
			&a.AvgLatencyMs,
		); err != nil {
			return nil, err
		}
		stats = append(stats, a)
	}
	return stats, rows.Err()
}

// GetRecords queries paginated raw usage logs with filtering.
func (s *Storage) GetRecords(filter RecordQueryFilter) ([]*Record, int64, error) {
	where, args := buildWhereClause(filter.QueryFilter)

	// 1. Count total matching rows
	countQuery := "SELECT COUNT(*) FROM usage_records" + where
	var total int64
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count records: %w", err)
	}

	// 2. Query paginated results
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := ClampPageSize(filter.PageSize)
	offset := (page - 1) * pageSize

	query := `
		SELECT
			id, provider, base_url, executor_type, model, alias,
			api_key, session_id, parent_session_id, auth_id, auth_index,
			auth_type, source, reasoning_effort, service_tier, generate,
			requested_at, latency_ms, ttft_ms, failed,
			failure_status_code, failure_body, fail_summary, header_error_kind, header_error_code,
			header_trace_id, header_quota_recover_at_ms, header_quota_used_percent, header_quota_window_minutes,
			header_secondary_quota_recover_at_ms, header_secondary_quota_used_percent, header_secondary_quota_window_minutes,
			rate_limit_reached_type, plan_type,
			response_model, model_mismatch,
			input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
			input_cost, output_cost, cache_read_cost, cache_creation_cost, total_cost,
			matched_model
		FROM usage_records
		` + where + `
		ORDER BY requested_at_unix DESC, id DESC
		LIMIT ? OFFSET ?
	`
	queryArgs := append(args, pageSize, offset)

	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query records: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		r := &Record{}
		var genInt, failedInt, mismatchInt int
		var reqAtStr string
		var quotaUsedPct, quotaWindowMinutes, secQuotaUsedPct, secQuotaWindowMinutes sql.NullFloat64
		var failSummary, headerErrorKind, headerErrorCode, headerTraceID, reachedType, planType, respModel sql.NullString

		if err := rows.Scan(
			&r.ID, &r.Provider, &r.BaseURL, &r.ExecutorType, &r.Model, &r.Alias,
			&r.APIKey, &r.SessionID, &r.ParentSessionID, &r.AuthID, &r.AuthIndex,
			&r.AuthType, &r.Source, &r.ReasoningEffort, &r.ServiceTier, &genInt,
			&reqAtStr, &r.LatencyMs, &r.TTFTMs, &failedInt,
			&r.FailureStatusCode, &r.FailureBody, &failSummary, &headerErrorKind, &headerErrorCode,
			&headerTraceID, &r.HeaderQuotaRecoverAtMS, &quotaUsedPct, &quotaWindowMinutes,
			&r.HeaderSecondaryQuotaRecoverAtMS, &secQuotaUsedPct, &secQuotaWindowMinutes,
			&reachedType, &planType,
			&respModel, &mismatchInt,
			&r.InputTokens, &r.OutputTokens, &r.ReasoningTokens,
			&r.CachedTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.TotalTokens,
			&r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheCreationCost, &r.TotalCost,
			&r.MatchedModel,
		); err != nil {
			return nil, 0, err
		}

		r.Generate = genInt == 1
		r.Failed = failedInt == 1
		r.ModelMismatch = mismatchInt == 1
		if respModel.Valid {
			r.ResponseModel = respModel.String
		}
		if failSummary.Valid {
			r.FailSummary = failSummary.String
		}
		if headerErrorKind.Valid {
			r.HeaderErrorKind = headerErrorKind.String
		}
		if headerErrorCode.Valid {
			r.HeaderErrorCode = headerErrorCode.String
		}
		if headerTraceID.Valid {
			r.HeaderTraceID = headerTraceID.String
		}
		if quotaUsedPct.Valid {
			r.HeaderQuotaUsedPercent = &quotaUsedPct.Float64
		}
		if quotaWindowMinutes.Valid {
			r.HeaderQuotaWindowMinutes = &quotaWindowMinutes.Float64
		}
		if secQuotaUsedPct.Valid {
			r.HeaderSecondaryQuotaUsedPercent = &secQuotaUsedPct.Float64
		}
		if secQuotaWindowMinutes.Valid {
			r.HeaderSecondaryQuotaWindowMinutes = &secQuotaWindowMinutes.Float64
		}
		if reachedType.Valid {
			r.RateLimitReachedType = reachedType.String
		}
		if planType.Valid {
			r.PlanType = planType.String
		}
		if t, err := time.Parse("2006-01-02 15:04:05", reqAtStr); err == nil {
			r.RequestedAt = t
		}

		records = append(records, r)
	}

	return records, total, rows.Err()
}

// SaveCustomPrice persists a custom price override.
func (s *Storage) SaveCustomPrice(r CustomPriceRecord) error {
	stmt := `
		INSERT INTO custom_prices (
			model, input_cost_per_token, output_cost_per_token,
			cache_read_cost_per_token, cache_creation_cost_per_token,
			cache_creation_input_token_cost_above_1hr, supports_prompt_caching,
			source, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET
			input_cost_per_token=excluded.input_cost_per_token,
			output_cost_per_token=excluded.output_cost_per_token,
			cache_read_cost_per_token=excluded.cache_read_cost_per_token,
			cache_creation_cost_per_token=excluded.cache_creation_cost_per_token,
			cache_creation_input_token_cost_above_1hr=excluded.cache_creation_input_token_cost_above_1hr,
			supports_prompt_caching=excluded.supports_prompt_caching,
			source=excluded.source,
			updated_at=excluded.updated_at
	`
	cachingInt := 0
	if r.SupportsPromptCaching {
		cachingInt = 1
	}
	_, err := s.db.Exec(stmt,
		strings.ToLower(strings.TrimSpace(r.Model)),
		r.InputCostPerToken, r.OutputCostPerToken,
		r.CacheReadCostPerToken, r.CacheCreationCostPerToken,
		r.CacheCreationInputTokenCostAbove1hr, cachingInt,
		r.Source, r.UpdatedAt,
	)
	return err
}

// DeleteCustomPrice removes a custom price override.
func (s *Storage) DeleteCustomPrice(model string) error {
	_, err := s.db.Exec("DELETE FROM custom_prices WHERE model = ?", strings.ToLower(strings.TrimSpace(model)))
	return err
}

// LoadCustomPrices reads all persisted custom price overrides.
func (s *Storage) LoadCustomPrices() ([]CustomPriceRecord, error) {
	rows, err := s.db.Query(`
		SELECT model, input_cost_per_token, output_cost_per_token,
		       cache_read_cost_per_token, cache_creation_cost_per_token,
		       cache_creation_input_token_cost_above_1hr, supports_prompt_caching,
		       source, updated_at
		FROM custom_prices
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []CustomPriceRecord
	for rows.Next() {
		var r CustomPriceRecord
		var cachingInt int
		if err := rows.Scan(
			&r.Model, &r.InputCostPerToken, &r.OutputCostPerToken,
			&r.CacheReadCostPerToken, &r.CacheCreationCostPerToken,
			&r.CacheCreationInputTokenCostAbove1hr, &cachingInt,
			&r.Source, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		r.SupportsPromptCaching = cachingInt == 1
		records = append(records, r)
	}
	return records, rows.Err()
}

// SaveSyncedPricesBatch persists a batch of synced prices.
func (s *Storage) SaveSyncedPricesBatch(records []SyncedPriceRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO synced_prices (
			model, input_cost_per_token, output_cost_per_token,
			cache_read_cost_per_token, cache_creation_cost_per_token,
			cache_creation_input_token_cost_above_1hr, supports_prompt_caching,
			source, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET
			input_cost_per_token=excluded.input_cost_per_token,
			output_cost_per_token=excluded.output_cost_per_token,
			cache_read_cost_per_token=excluded.cache_read_cost_per_token,
			cache_creation_cost_per_token=excluded.cache_creation_cost_per_token,
			cache_creation_input_token_cost_above_1hr=excluded.cache_creation_input_token_cost_above_1hr,
			supports_prompt_caching=excluded.supports_prompt_caching,
			source=excluded.source,
			updated_at=excluded.updated_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range records {
		cachingInt := 0
		if r.SupportsPromptCaching {
			cachingInt = 1
		}
		if _, err := stmt.Exec(
			strings.ToLower(strings.TrimSpace(r.Model)),
			r.InputCostPerToken, r.OutputCostPerToken,
			r.CacheReadCostPerToken, r.CacheCreationCostPerToken,
			r.CacheCreationInputTokenCostAbove1hr, cachingInt,
			r.Source, r.UpdatedAt,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// LoadSyncedPrices reads all persisted synced prices.
func (s *Storage) LoadSyncedPrices() ([]SyncedPriceRecord, error) {
	rows, err := s.db.Query(`
		SELECT model, input_cost_per_token, output_cost_per_token,
		       cache_read_cost_per_token, cache_creation_cost_per_token,
		       cache_creation_input_token_cost_above_1hr, supports_prompt_caching,
		       source, updated_at
		FROM synced_prices
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []SyncedPriceRecord
	for rows.Next() {
		var r SyncedPriceRecord
		var cachingInt int
		if err := rows.Scan(
			&r.Model, &r.InputCostPerToken, &r.OutputCostPerToken,
			&r.CacheReadCostPerToken, &r.CacheCreationCostPerToken,
			&r.CacheCreationInputTokenCostAbove1hr, &cachingInt,
			&r.Source, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		r.SupportsPromptCaching = cachingInt == 1
		records = append(records, r)
	}
	return records, rows.Err()
}

// GetFilterOptions retrieves distinct filter dropdown choices.
func (s *Storage) GetFilterOptions() (*FilterOptions, error) {
	opts := &FilterOptions{
		Models:    make([]string, 0),
		Providers: make([]string, 0),
		APIKeys:   make([]string, 0),
		AuthIDs:   make([]string, 0),
	}

	// Models
	mRows, err := s.db.Query("SELECT DISTINCT model FROM usage_records WHERE model != '' ORDER BY model ASC")
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var v string
			if err := mRows.Scan(&v); err == nil && v != "" {
				opts.Models = append(opts.Models, v)
			}
		}
	}

	// Providers
	pRows, err := s.db.Query("SELECT DISTINCT provider FROM usage_records WHERE provider != '' ORDER BY provider ASC")
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var v string
			if err := pRows.Scan(&v); err == nil && v != "" {
				opts.Providers = append(opts.Providers, v)
			}
		}
	}

	// APIKeys
	kRows, err := s.db.Query("SELECT DISTINCT api_key FROM usage_records WHERE api_key != '' ORDER BY api_key ASC")
	if err == nil {
		defer kRows.Close()
		for kRows.Next() {
			var v string
			if err := kRows.Scan(&v); err == nil && v != "" {
				opts.APIKeys = append(opts.APIKeys, v)
			}
		}
	}

	// AuthIDs
	aRows, err := s.db.Query("SELECT DISTINCT auth_id FROM usage_records WHERE auth_id != '' ORDER BY auth_id ASC")
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var v string
			if err := aRows.Scan(&v); err == nil && v != "" {
				opts.AuthIDs = append(opts.AuthIDs, v)
			}
		}
	}

	return opts, nil
}

// ClearAll removes all records from the database.
func (s *Storage) ClearAll() (int64, error) {
	res, err := s.db.Exec("DELETE FROM usage_records")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Cleanup removes records older than the given cutoff time.
func (s *Storage) Cleanup(before time.Time) (int64, error) {
	res, err := s.db.Exec("DELETE FROM usage_records WHERE requested_at_unix < ?", before.UTC().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Ping checks database health.
func (s *Storage) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// GetDiagnosticStats calculates error telemetry, status code distributions, and recent failure logs.
func (s *Storage) GetDiagnosticStats(filter QueryFilter, recentLimit int) (*DiagnosticStats, error) {
	if recentLimit <= 0 {
		recentLimit = 50
	}
	// Recent errors are fetched through GetRecords, which caps a page at
	// MaxPageSize. Clamp here as well so the caller is not promised more
	// rows than can actually be returned.
	if recentLimit > MaxPageSize {
		recentLimit = MaxPageSize
	}

	where, args := buildWhereClause(filter)

	// Total & Failed counts
	countsQuery := `
		SELECT 
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END), 0)
		FROM usage_records
	` + where

	var totalReqs, failedReqs int64
	if err := s.db.QueryRow(countsQuery, args...).Scan(&totalReqs, &failedReqs); err != nil {
		return nil, fmt.Errorf("failed to get diagnostic counts: %w", err)
	}

	stats := &DiagnosticStats{
		TotalRequests:  totalReqs,
		FailedRequests: failedReqs,
		ByStatusCode:   make([]DiagnosticErrorGroup, 0),
		ByErrorKind:    make([]DiagnosticErrorGroup, 0),
		ByErrorCode:    make([]DiagnosticErrorGroup, 0),
		ByProvider:     make([]DiagnosticErrorGroup, 0),
		ByModel:        make([]DiagnosticErrorGroup, 0),
		RecentErrors:   make([]*Record, 0),
	}

	if totalReqs > 0 {
		stats.FailureRate = float64(failedReqs) / float64(totalReqs)
	}

	if failedReqs == 0 {
		return stats, nil
	}

	// Filter for failures
	failedFilter := filter
	failedVal := true
	failedFilter.Failed = &failedVal
	failedWhere, failedArgs := buildWhereClause(failedFilter)

	// Helper to query grouping
	queryGrouping := func(colExpr, filterCondition string, limit int) ([]DiagnosticErrorGroup, error) {
		cond := failedWhere
		if filterCondition != "" {
			if cond == "" {
				cond = " WHERE " + filterCondition
			} else {
				cond = cond + " AND " + filterCondition
			}
		}
		q := fmt.Sprintf(`
			SELECT %s AS grp_key, COUNT(*) AS grp_count
			FROM usage_records
			%s
			GROUP BY grp_key
			ORDER BY grp_count DESC
			LIMIT %d
		`, colExpr, cond, limit)

		rows, err := s.db.Query(q, failedArgs...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var groups []DiagnosticErrorGroup
		for rows.Next() {
			var k sql.NullString
			var c int64
			if err := rows.Scan(&k, &c); err == nil {
				keyStr := k.String
				if !k.Valid || keyStr == "" {
					keyStr = "unknown"
				}
				groups = append(groups, DiagnosticErrorGroup{Key: keyStr, Count: c})
			}
		}
		return groups, rows.Err()
	}

	// By status code
	if g, err := queryGrouping("CAST(failure_status_code AS TEXT)", "failure_status_code > 0", 20); err == nil {
		stats.ByStatusCode = g
	}

	// By error kind
	if g, err := queryGrouping("header_error_kind", "header_error_kind != ''", 20); err == nil {
		stats.ByErrorKind = g
	}

	// By error code
	if g, err := queryGrouping("header_error_code", "header_error_code != ''", 20); err == nil {
		stats.ByErrorCode = g
	}

	// By provider
	if g, err := queryGrouping("provider", "", 20); err == nil {
		stats.ByProvider = g
	}

	// By model
	if g, err := queryGrouping("model", "", 20); err == nil {
		stats.ByModel = g
	}

	// Model Mismatches count
	var mismatches int64
	mismatchWhere, mismatchArgs := buildWhereClause(filter)
	mismatchCond := "WHERE model_mismatch = 1"
	if mismatchWhere != "" {
		mismatchCond = mismatchWhere + " AND model_mismatch = 1"
	}
	_ = s.db.QueryRow("SELECT COUNT(*) FROM usage_records "+mismatchCond, mismatchArgs...).Scan(&mismatches)
	stats.ModelMismatches = mismatches

	// By Mismatch Pair (e.g. gpt-4o -> gpt-4o-mini)
	mismatchRows, err := s.db.Query(`
		SELECT (model || ' -> ' || response_model) AS grp_key, COUNT(*) AS grp_count
		FROM usage_records
		`+mismatchCond+`
		GROUP BY grp_key
		ORDER BY grp_count DESC
		LIMIT 20
	`, mismatchArgs...)
	if err == nil {
		defer mismatchRows.Close()
		var pairs []DiagnosticErrorGroup
		for mismatchRows.Next() {
			var k sql.NullString
			var c int64
			if err := mismatchRows.Scan(&k, &c); err == nil && k.Valid && k.String != "" {
				pairs = append(pairs, DiagnosticErrorGroup{Key: k.String, Count: c})
			}
		}
		stats.ByMismatchPair = pairs
	}

	// Recent errors
	recentRecords, _, err := s.GetRecords(RecordQueryFilter{
		QueryFilter: failedFilter,
		Page:        1,
		PageSize:    recentLimit,
	})
	if err == nil && recentRecords != nil {
		stats.RecentErrors = recentRecords
	}

	return stats, nil
}

// ExportRecords queries usage records for export matching the given filter.
func (s *Storage) ExportRecords(filter QueryFilter, limit int) ([]*Record, error) {
	limit = ClampExportLimit(limit)
	where, args := buildWhereClause(filter)
	query := `
		SELECT
			id, provider, base_url, executor_type, model, alias,
			api_key, session_id, parent_session_id, auth_id, auth_index,
			auth_type, source, reasoning_effort, service_tier, generate,
			requested_at, latency_ms, ttft_ms, failed,
			failure_status_code, failure_body, fail_summary, header_error_kind, header_error_code,
			header_trace_id, header_quota_recover_at_ms, header_quota_used_percent, header_quota_window_minutes,
			header_secondary_quota_recover_at_ms, header_secondary_quota_used_percent, header_secondary_quota_window_minutes,
			rate_limit_reached_type, plan_type,
			response_model, model_mismatch,
			input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
			input_cost, output_cost, cache_read_cost, cache_creation_cost, total_cost,
			matched_model
		FROM usage_records
		` + where + `
		ORDER BY requested_at_unix ASC, id ASC
		LIMIT ?
	`
	queryArgs := append(args, limit)

	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query records for export: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		r := &Record{}
		var genInt, failedInt, mismatchInt int
		var reqAtStr string
		var quotaUsedPct, quotaWindowMinutes, secQuotaUsedPct, secQuotaWindowMinutes sql.NullFloat64
		var failSummary, headerErrorKind, headerErrorCode, headerTraceID, reachedType, planType, respModel sql.NullString

		if err := rows.Scan(
			&r.ID, &r.Provider, &r.BaseURL, &r.ExecutorType, &r.Model, &r.Alias,
			&r.APIKey, &r.SessionID, &r.ParentSessionID, &r.AuthID, &r.AuthIndex,
			&r.AuthType, &r.Source, &r.ReasoningEffort, &r.ServiceTier, &genInt,
			&reqAtStr, &r.LatencyMs, &r.TTFTMs, &failedInt,
			&r.FailureStatusCode, &r.FailureBody, &failSummary, &headerErrorKind, &headerErrorCode,
			&headerTraceID, &r.HeaderQuotaRecoverAtMS, &quotaUsedPct, &quotaWindowMinutes,
			&r.HeaderSecondaryQuotaRecoverAtMS, &secQuotaUsedPct, &secQuotaWindowMinutes,
			&reachedType, &planType,
			&respModel, &mismatchInt,
			&r.InputTokens, &r.OutputTokens, &r.ReasoningTokens,
			&r.CachedTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.TotalTokens,
			&r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheCreationCost, &r.TotalCost,
			&r.MatchedModel,
		); err != nil {
			return nil, err
		}

		r.Generate = genInt == 1
		r.Failed = failedInt == 1
		r.ModelMismatch = mismatchInt == 1
		if respModel.Valid {
			r.ResponseModel = respModel.String
		}
		if failSummary.Valid {
			r.FailSummary = failSummary.String
		}
		if headerErrorKind.Valid {
			r.HeaderErrorKind = headerErrorKind.String
		}
		if headerErrorCode.Valid {
			r.HeaderErrorCode = headerErrorCode.String
		}
		if headerTraceID.Valid {
			r.HeaderTraceID = headerTraceID.String
		}
		if quotaUsedPct.Valid {
			r.HeaderQuotaUsedPercent = &quotaUsedPct.Float64
		}
		if quotaWindowMinutes.Valid {
			r.HeaderQuotaWindowMinutes = &quotaWindowMinutes.Float64
		}
		if secQuotaUsedPct.Valid {
			r.HeaderSecondaryQuotaUsedPercent = &secQuotaUsedPct.Float64
		}
		if secQuotaWindowMinutes.Valid {
			r.HeaderSecondaryQuotaWindowMinutes = &secQuotaWindowMinutes.Float64
		}
		if reachedType.Valid {
			r.RateLimitReachedType = reachedType.String
		}
		if planType.Valid {
			r.PlanType = planType.String
		}
		if t, err := time.Parse("2006-01-02 15:04:05", reqAtStr); err == nil {
			r.RequestedAt = t
		}

		records = append(records, r)
	}

	return records, rows.Err()
}

// ImportRecords batch inserts imported records and skips duplicate items.
func (s *Storage) ImportRecords(records []*Record) (int, int, error) {
	if len(records) == 0 {
		return 0, 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to begin import tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmtCheckSession, err := tx.Prepare("SELECT 1 FROM usage_records WHERE session_id = ? LIMIT 1")
	if err != nil {
		return 0, 0, fmt.Errorf("prepare session check failed: %w", err)
	}
	defer stmtCheckSession.Close()

	stmtCheckSig, err := tx.Prepare(`
		SELECT 1 FROM usage_records 
		WHERE requested_at_unix = ? AND model = ? AND auth_id = ? AND total_tokens = ? AND latency_ms = ? 
		LIMIT 1
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("prepare signature check failed: %w", err)
	}
	defer stmtCheckSig.Close()

	insertStmt, err := tx.Prepare(`
		INSERT INTO usage_records (
			provider, base_url, executor_type, model, alias,
			api_key, session_id, parent_session_id, auth_id, auth_index,
			auth_type, source, reasoning_effort, service_tier, generate,
			requested_at, requested_at_unix, latency_ms, ttft_ms, failed,
			failure_status_code, failure_body, fail_summary, header_error_kind, header_error_code,
			header_trace_id, header_quota_recover_at_ms, header_quota_used_percent, header_quota_window_minutes,
			header_secondary_quota_recover_at_ms, header_secondary_quota_used_percent, header_secondary_quota_window_minutes,
			rate_limit_reached_type, plan_type,
			response_model, model_mismatch,
			input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
			input_cost, output_cost, cache_read_cost, cache_creation_cost, total_cost,
			matched_model
		) VALUES (
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?
		)
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("prepare insert stmt failed: %w", err)
	}
	defer insertStmt.Close()

	imported := 0
	skipped := 0

	for _, r := range records {
		reqAt := r.RequestedAt
		if reqAt.IsZero() {
			reqAt = time.Now()
		}
		reqAtUTC := reqAt.UTC()
		unixSec := reqAtUTC.Unix()

		// 1. Deduplication check
		isDup := false
		if r.SessionID != "" {
			var dummy int
			if err := stmtCheckSession.QueryRow(r.SessionID).Scan(&dummy); err == nil {
				isDup = true
			}
		} else {
			var dummy int
			if err := stmtCheckSig.QueryRow(unixSec, r.Model, r.AuthID, r.TotalTokens, r.LatencyMs).Scan(&dummy); err == nil {
				isDup = true
			}
		}

		if isDup {
			skipped++
			continue
		}

		genInt := 0
		if r.Generate {
			genInt = 1
		}
		failedInt := 0
		if r.Failed {
			failedInt = 1
		}
		mismatchInt := 0
		if r.ModelMismatch {
			mismatchInt = 1
		}

		if _, err := insertStmt.Exec(
			r.Provider, r.BaseURL, r.ExecutorType, r.Model, r.Alias,
			r.APIKey, r.SessionID, r.ParentSessionID, r.AuthID, r.AuthIndex,
			r.AuthType, r.Source, r.ReasoningEffort, r.ServiceTier, genInt,
			reqAtUTC.Format("2006-01-02 15:04:05"), unixSec, r.LatencyMs, r.TTFTMs, failedInt,
			r.FailureStatusCode, r.FailureBody, r.FailSummary, r.HeaderErrorKind, r.HeaderErrorCode,
			r.HeaderTraceID, r.HeaderQuotaRecoverAtMS, r.HeaderQuotaUsedPercent, r.HeaderQuotaWindowMinutes,
			r.HeaderSecondaryQuotaRecoverAtMS, r.HeaderSecondaryQuotaUsedPercent, r.HeaderSecondaryQuotaWindowMinutes,
			r.RateLimitReachedType, r.PlanType,
			r.ResponseModel, mismatchInt,
			r.InputTokens, r.OutputTokens, r.ReasoningTokens,
			r.CachedTokens, r.CacheReadTokens, r.CacheCreationTokens, r.TotalTokens,
			r.InputCost, r.OutputCost, r.CacheReadCost, r.CacheCreationCost, r.TotalCost,
			r.MatchedModel,
		); err != nil {
			return imported, skipped, fmt.Errorf("failed to insert imported record: %w", err)
		}
		imported++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("failed to commit import tx: %w", err)
	}

	return imported, skipped, nil
}

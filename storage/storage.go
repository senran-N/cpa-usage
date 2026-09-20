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
	`
	_, err := s.db.Exec(schema)
	return err
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
			failure_status_code, failure_body, input_tokens, output_tokens, reasoning_tokens,
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
			?, ?, ?, ?, ?,
			?
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

		res, err := stmt.Exec(
			r.Provider, r.BaseURL, r.ExecutorType, r.Model, r.Alias,
			r.APIKey, r.SessionID, r.ParentSessionID, r.AuthID, r.AuthIndex,
			r.AuthType, r.Source, r.ReasoningEffort, r.ServiceTier, genInt,
			reqAtUTC.Format("2006-01-02 15:04:05"), reqAtUTC.Unix(), r.LatencyMs, r.TTFTMs, failedInt,
			r.FailureStatusCode, r.FailureBody, r.InputTokens, r.OutputTokens, r.ReasoningTokens,
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
	pageSize := filter.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	query := `
		SELECT
			id, provider, base_url, executor_type, model, alias,
			api_key, session_id, parent_session_id, auth_id, auth_index,
			auth_type, source, reasoning_effort, service_tier, generate,
			requested_at, latency_ms, ttft_ms, failed,
			failure_status_code, failure_body, input_tokens, output_tokens, reasoning_tokens,
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
		var genInt, failedInt int
		var reqAtStr string

		if err := rows.Scan(
			&r.ID, &r.Provider, &r.BaseURL, &r.ExecutorType, &r.Model, &r.Alias,
			&r.APIKey, &r.SessionID, &r.ParentSessionID, &r.AuthID, &r.AuthIndex,
			&r.AuthType, &r.Source, &r.ReasoningEffort, &r.ServiceTier, &genInt,
			&reqAtStr, &r.LatencyMs, &r.TTFTMs, &failedInt,
			&r.FailureStatusCode, &r.FailureBody, &r.InputTokens, &r.OutputTokens, &r.ReasoningTokens,
			&r.CachedTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.TotalTokens,
			&r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheCreationCost, &r.TotalCost,
			&r.MatchedModel,
		); err != nil {
			return nil, 0, err
		}

		r.Generate = genInt == 1
		r.Failed = failedInt == 1
		if t, err := time.Parse("2006-01-02 15:04:05", reqAtStr); err == nil {
			r.RequestedAt = t
		}

		records = append(records, r)
	}

	return records, total, rows.Err()
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

package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/jmoiron/sqlx"
)

type ScopeFunc func(fields []string, res any) map[string]any

// Option configures optional behavior for NewDatabase. All options are
// additive and opt-in — calling NewDatabase with zero options behaves
// exactly as before this patch.
type Option func(*dbOptions)

type dbOptions struct {
	extraLoggers       []QueryLogger
	preparedStatements bool
	preparedCacheSize  int
}

// WithQueryLogger registers an additional QueryLogger that runs alongside
// the default file/Telegram logger (it does not replace it). This is the
// hook for #4 (observability): build one via db.NewMetricsQueryLogger(sink)
// to feed a MetricsSink, or pass any other QueryLogger implementation.
func WithQueryLogger(l QueryLogger) Option {
	return func(o *dbOptions) {
		if l != nil {
			o.extraLoggers = append(o.extraLoggers, l)
		}
	}
}

// WithPreparedStatements turns on the prepared-statement cache for plain
// (non-transaction) queries. cacheSize <= 0 defaults to 200 entries. This
// is opt-in and off by default — see internal/db/prepared.go for what it
// does and does not help with.
func WithPreparedStatements(cacheSize int) Option {
	return func(o *dbOptions) {
		o.preparedStatements = true
		o.preparedCacheSize = cacheSize
	}
}

type DB struct {
	dialect   Dialect
	executor  Executor
	logger    QueryLogger
	closeFunc func() error
	ctx       context.Context
	scope     ScopeFunc

	preparedCache *preparedCache // non-nil only if WithPreparedStatements was used; closed on DB.Close()
}

func NewDatabase(sqlxDB *sqlx.DB, dialect Dialect, closeFunc func() error, scope ScopeFunc, opts ...Option) *DB {
	cfg := &dbOptions{}
	for _, o := range opts {
		if o != nil {
			o(cfg)
		}
	}

	var exec Executor
	var preparedCacheRef *preparedCache
	if cfg.preparedStatements {
		pe := newPreparedExecutor(sqlxDB, cfg.preparedCacheSize)
		exec = pe
		preparedCacheRef = pe.cache
	} else {
		exec = &sqlxExecutor{db: sqlxDB}
	}

	loggers := append([]QueryLogger{FrameworkLogger()}, cfg.extraLoggers...)
	var ql QueryLogger
	if len(loggers) == 1 {
		ql = loggers[0]
	} else {
		ql = &multiLogger{loggers: loggers}
	}

	return &DB{
		executor:      NewLoggedExecutor(exec, ql),
		dialect:       dialect,
		logger:        ql,
		closeFunc:     closeFunc,
		scope:         scope,
		preparedCache: preparedCacheRef,
	}
}

func (db *DB) Close() error {
	if db.preparedCache != nil {
		_ = db.preparedCache.Close()
	}
	if db.closeFunc != nil {
		return db.closeFunc()
	}
	return nil
}

func (db *DB) Ping() error {
	exec := db.executor
	if le, ok := exec.(*LoggedExecutor); ok {
		exec = le.inner
	}
	if pe, ok := exec.(*preparedExecutor); ok {
		exec = pe.inner
	}
	if sxe, ok := exec.(*sqlxExecutor); ok {
		return sxe.db.Ping()
	}
	return fmt.Errorf("ping not supported on this executor type")
}

func (db *DB) Stats() sql.DBStats {
	exec := db.executor
	if le, ok := exec.(*LoggedExecutor); ok {
		exec = le.inner
	}
	if pe, ok := exec.(*preparedExecutor); ok {
		exec = pe.inner
	}
	if sxe, ok := exec.(*sqlxExecutor); ok {
		return sxe.db.Stats()
	}
	return sql.DBStats{}
}

func (db *DB) Insert(data any) error {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return fmt.Errorf("Insert: %w", err)
	}
	return db.Table(tableName).Insert(data)
}

func (db *DB) InsertBatch(data []any) (int64, error) {
	if len(data) == 0 {
		return 0, fmt.Errorf("InsertBatch: %w", ErrEmptyData)
	}
	tableName, err := getTableNameFromModel(data[0])
	if err != nil {
		return 0, fmt.Errorf("InsertBatch: %w", err)
	}
	return db.Table(tableName).InsertBatch(data)
}

func (db *DB) Upsert(data any) error {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return fmt.Errorf("Upsert: %w", err)
	}
	return db.Table(tableName).Upsert(data)
}

func (db *DB) Update(data any) error {
	return fmt.Errorf("Update: %w (use db.Where(...).Update() or db.UpdateAll())", ErrWhereClauseRequired)
}

func (db *DB) UpdateAll(data any) (int64, error) {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return 0, fmt.Errorf("UpdateAll: %w", err)
	}
	return db.Table(tableName).execUpdate(data, true)
}

func (db *DB) Delete(data any) error {
	return fmt.Errorf("Delete: %w (use db.Where(...).Delete() or db.DeleteAll())", ErrWhereClauseRequired)
}

func (db *DB) DeleteAll(data any) (int64, error) {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return 0, fmt.Errorf("DeleteAll: %w", err)
	}
	return db.Table(tableName).execDelete(data, true)
}

func (db *DB) Find(dest any) (bool, error) {
	tableName, err := getTableNameFromModel(dest)
	if err != nil {
		return false, fmt.Errorf("Find: %w", err)
	}
	return db.Table(tableName).Find(dest)
}

func (db *DB) FindAll(dest any) error {
	tableName, err := getTableNameFromModel(dest)
	if err != nil {
		return fmt.Errorf("FindAll: %w", err)
	}
	return db.Table(tableName).FindAll(dest)
}

func (db *DB) Paginate(dest any, page, perPage int) (*Pagination, error) {
	tableName, err := getTableNameFromModel(dest)
	if err != nil {
		return nil, fmt.Errorf("Paginate: %w", err)
	}
	return db.Table(tableName).Paginate(dest, page, perPage)
}

func (db *DB) Table(name string) *Builder {
	safeName := SanitizeIdentifier(name)
	if safeName == "" && name != "" {
		logger.LogError("[DB Security] Invalid table name blocked: %s", name)
	}
	return &Builder{
		table:    safeName,
		dialect:  db.dialect,
		executor: db.executor,
		ctx:      db.ctx,
		scope:    db.scope,
	}
}

func (db *DB) Where(expr string, args ...any) *Builder {
	return db.Table("").Where(expr, args...)
}

func (db *DB) Raw(query string, args ...any) *Builder {
	return &Builder{
		rawQuery: query,
		rawArgs:  args,
		dialect:  db.dialect,
		executor: db.executor,
		ctx:      db.ctx,
		scope:    db.scope,
	}
}

func (db *DB) NamedRaw(query string, args map[string]any) *Builder {
	return &Builder{
		rawQuery:  query,
		namedArgs: args,
		dialect:   db.dialect,
		executor:  db.executor,
		ctx:       db.ctx,
		scope:     db.scope,
	}
}

func (db *DB) Begin(ctx context.Context) (Tx, error) {
	if ctx == nil && db.ctx != nil {
		ctx = db.ctx
	}
	return db.executor.Begin(ctx)
}

func (db *DB) Transaction(ctx context.Context, fn func(tx Tx) error) (err error) {
	if ctx == nil && db.ctx != nil {
		ctx = db.ctx
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			logger.LogError("[DB] Transaction rollback error: %v", rbErr)
		}
		return err
	}

	return tx.Commit()
}

func (db *DB) WithContext(ctx context.Context) *DB {
	return &DB{
		dialect:   db.dialect,
		executor:  db.executor,
		logger:    db.logger,
		closeFunc: nil,
		ctx:       ctx,
		scope:     db.scope,
	}
}

func (db *DB) Use(tx Tx) *DB {
	return &DB{
		dialect:   db.dialect,
		executor:  tx,
		logger:    db.logger,
		closeFunc: nil,
		ctx:       db.ctx,
		scope:     db.scope,
	}
}

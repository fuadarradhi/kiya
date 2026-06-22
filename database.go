package kiya

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

type Result interface {
	LastInsertId() (int64, error)
	RowsAffected() (int64, error)
}

type insertResult struct {
	lastInsertId int64
	rowsAffected int64
}

func (r *insertResult) LastInsertId() (int64, error) { return r.lastInsertId, nil }
func (r *insertResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }

type Tx interface {
	Executor
	Commit() error
	Rollback() error
}

type Executor interface {
	Select(ctx context.Context, dest any, query string, args ...any) error
	Get(ctx context.Context, dest any, query string, args ...any) error
	Exec(ctx context.Context, query string, args ...any) (Result, error)
	Begin(ctx context.Context) (Tx, error)
}

type DB struct {
	dialect          Dialect
	executor         Executor
	logger           QueryLogger
	closeFunc        func() error
	ctx              context.Context
	defaultCondition DefaultConditionFunc
}

func NewDB(exec Executor, dialect Dialect, logger QueryLogger, closeFunc func() error, defaultCondition DefaultConditionFunc) *DB {
	if logger != nil {
		exec = &LoggedExecutor{inner: exec, logger: logger}
	}
	return &DB{executor: exec, dialect: dialect, logger: logger, closeFunc: closeFunc, defaultCondition: defaultCondition}
}

func (db *DB) WithContext(ctx context.Context) *DB {
	return &DB{
		dialect:          db.dialect,
		executor:         db.executor,
		logger:           db.logger,
		closeFunc:        nil,
		ctx:              ctx,
		defaultCondition: db.defaultCondition,
	}
}

func (db *DB) Use(tx Tx) *DB {
	return &DB{
		dialect:          db.dialect,
		executor:         tx,
		logger:           db.logger,
		closeFunc:        nil,
		ctx:              db.ctx,
		defaultCondition: db.defaultCondition,
	}
}

func NewDatabase(cfg DatabaseConfig) (*DB, error) {
	if !cfg.Enabled {
		LogInfo("[Goserver] Database is disabled via config")
		return nil, nil
	}

	tz := cfg.Timezone
	if tz == "" {
		tz = "UTC"
	}

	host := cfg.Host
	if host == "" {
		host = "localhost"
	}

	var dsn string
	var dialect Dialect
	var driverName string

	switch cfg.Driver {
	case "mysql":
		dialect = MySQLDialect{}
		driverName = "mysql"
		locParam := strings.ReplaceAll(tz, "/", "%2F")
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=%s",
			cfg.User,
			cfg.Password,
			host,
			cfg.Port,
			cfg.Name,
			locParam,
		)

	case "postgres":
		dialect = PostgresDialect{}
		driverName = "pgx"

		sslMode := "require"
		if host == "localhost" || host == "127.0.0.1" {
			sslMode = "disable"
		}

		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
			host, cfg.Port, cfg.User, cfg.Password, cfg.Name, sslMode, tz)

	default:
		return nil, fmt.Errorf("driver database tidak didukung: '%s'. Hanya 'mysql' atau 'postgres' yang tersedia", cfg.Driver)
	}

	var db *sqlx.DB
	var err error

	db, err = sqlx.Connect(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("gagal membuat koneksi database (%s): %w", cfg.Driver, err)
	}

	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 50
	}
	db.SetMaxOpenConns(maxOpen)

	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 10
	}
	db.SetMaxIdleConns(maxIdle)

	connMaxLifetime := cfg.ConnMaxLifetime
	if connMaxLifetime <= 0 {
		connMaxLifetime = 5 * time.Minute
	}
	db.SetConnMaxLifetime(connMaxLifetime)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("gagal melakukan ping ke database (%s): %w", cfg.Driver, err)
	}

	LogInfo("[Goserver] Database connected | Driver: %s | Host: %s:%s | DB: %s | TZ: %s", cfg.Driver, host, cfg.Port, cfg.Name, tz)

	exec := &sqlxExecutor{db: db}

	return NewDB(exec, dialect, &frameworkLogger{}, db.Close, cfg.DefaultCondition), nil
}

func (db *DB) Close() error {
	if db.closeFunc != nil {
		return db.closeFunc()
	}
	return nil
}

func (db *DB) Insert(data any) (Result, error) {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return nil, fmt.Errorf("Insert: %w", err)
	}
	return db.Table(tableName).Insert(data)
}

func (db *DB) Update(data any) (Result, error) {
	return nil, errors.New("Update requires a Where clause. Use db.Where(...).Update() or db.UpdateAll()")
}

func (db *DB) UpdateAll(data any) (Result, error) {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return nil, fmt.Errorf("UpdateAll: %w", err)
	}
	return db.Table(tableName).execUpdate(data, true)
}

func (db *DB) Delete(data any) (Result, error) {
	return nil, errors.New("Delete requires a Where clause. Use db.Where(...).Delete() or db.DeleteAll()")
}

func (db *DB) DeleteAll(data any) (Result, error) {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return nil, fmt.Errorf("DeleteAll: %w", err)
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

func (db *DB) Table(name string) *Builder {
	safeName := sanitizeIdentifier(name)
	if safeName == "" && name != "" {
		LogError("[DB Security] Invalid table name blocked: %s", name)
	}
	return &Builder{
		table:            safeName,
		dialect:          db.dialect,
		executor:         db.executor,
		ctx:              db.ctx,
		defaultCondition: db.defaultCondition,
	}
}

func (db *DB) Where(expr string, args ...any) *Builder {
	return db.Table("").Where(expr, args...)
}

func (db *DB) Raw(query string, args ...any) *Builder {
	return &Builder{
		rawQuery:         query,
		rawArgs:          args,
		dialect:          db.dialect,
		executor:         db.executor,
		ctx:              db.ctx,
		defaultCondition: db.defaultCondition,
	}
}

func (db *DB) NamedRaw(query string, args map[string]any) *Builder {
	return &Builder{
		rawQuery:         query,
		namedArgs:        args,
		dialect:          db.dialect,
		executor:         db.executor,
		ctx:              db.ctx,
		defaultCondition: db.defaultCondition,
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
			LogError("[DB] Transaction rollback error: %v", rbErr)
		}
		return err
	}

	return tx.Commit()
}

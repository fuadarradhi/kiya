package kiya

import (
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"

	"github.com/fuadarradhi/kiya/internal/db"
	"github.com/fuadarradhi/kiya/internal/logger"
)

// Result is an alias for db.Result
type Result = db.Result

// Tx is an alias for db.Tx
type Tx = db.Tx

// Executor is an alias for db.Executor
type Executor = db.Executor

// Dialect is an alias for db.Dialect
type Dialect = db.Dialect

// DB is an alias for db.DB. All fields are unexported.
type DB = db.DB

// Builder is an alias for db.Builder. All fields are unexported.
type Builder = db.Builder

// Pagination is an alias for db.Pagination
type Pagination = db.Pagination

// NewDatabase creates a new database connection based on the provided config.
func NewDatabase(cfg DatabaseConfig) (*DB, error) {
	if !cfg.Enabled {
		logger.LogInfo("[Kiya] Database is disabled via config")
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
	var dialect db.Dialect
	var driverName string

	switch cfg.Driver {
	case "mysql":
		dialect = db.MySQLDialect{}
		driverName = "mysql"
		locParam := strings.ReplaceAll(tz, "/", "%2F")
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=%s",
			cfg.User, cfg.Password, host, cfg.Port, cfg.Name, locParam)

	case "postgres":
		dialect = db.PostgresDialect{}
		driverName = "pgx"

		sslMode := "require"
		if host == "localhost" || host == "127.0.0.1" {
			sslMode = "disable"
		}

		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
			host, cfg.Port, cfg.User, cfg.Password, cfg.Name, sslMode, tz)

	default:
		return nil, fmt.Errorf("unsupported database driver: '%s'. Only 'mysql' or 'postgres' are available", cfg.Driver)
	}

	sqlxDB, err := sqlx.Connect(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to create database connection (%s): %w", cfg.Driver, err)
	}

	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 50
	}
	sqlxDB.SetMaxOpenConns(maxOpen)

	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 10
	}
	sqlxDB.SetMaxIdleConns(maxIdle)

	connMaxLifetime := cfg.ConnMaxLifetime
	if connMaxLifetime <= 0 {
		connMaxLifetime = 5 * time.Minute
	}
	sqlxDB.SetConnMaxLifetime(connMaxLifetime)

	if err := sqlxDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database (%s): %w", cfg.Driver, err)
	}

	logger.LogInfo("[Kiya] Database connected | Driver: %s | Host: %s:%s | DB: %s | TZ: %s", cfg.Driver, host, cfg.Port, cfg.Name, tz)

	var dbDefaultCond db.DefaultConditionFunc
	if cfg.DefaultCondition != nil {
		dbDefaultCond = func(fields []string, res any) map[string]any {
			if r, ok := res.(*Resources); ok {
				return cfg.DefaultCondition(fields, r)
			}
			return nil
		}
	}

	return db.NewDatabase(sqlxDB, dialect, sqlxDB.Close, dbDefaultCond), nil
}

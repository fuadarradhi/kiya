package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fuadarradhi/kiya/internal/ctxkey"
	"github.com/fuadarradhi/kiya/internal/logger"
)

var reLogPlaceholder = regexp.MustCompile(`('(?:[^']|'')*')|(\?)`)

type QueryLog struct {
	Query    string
	Args     []any
	Duration time.Duration
	Err      error
	Rows     int64
	Context  map[string]any
}

type QueryLogger interface {
	Log(q QueryLog)
}

type frameworkLogger struct{}

func (l *frameworkLogger) Log(q QueryLog) {
	if q.Err != nil {
		logger.LogError("[DB] %s | Error: %v | Duration: %s", q.Query, q.Err, q.Duration)
	} else {
		logger.LogInfo("[DB] %s | Rows: %d | Duration: %s", q.Query, q.Rows, q.Duration)
	}
}

func FrameworkLogger() QueryLogger {
	return &frameworkLogger{}
}

type loggedTx struct {
	inner  Tx
	logger QueryLogger
}

func (t *loggedTx) Select(ctx context.Context, dest any, query string, args ...any) error {
	start := time.Now()
	err := t.inner.Select(ctx, dest, query, args...)
	if t.logger != nil {
		t.logger.Log(QueryLog{
			Query:    query,
			Args:     args,
			Duration: time.Since(start),
			Err:      err,
			Context:  ctxToMap(ctx),
		})
	}
	return err
}

func (t *loggedTx) Get(ctx context.Context, dest any, query string, args ...any) error {
	start := time.Now()
	err := t.inner.Get(ctx, dest, query, args...)
	if t.logger != nil {
		logErr := err
		if errors.Is(err, sql.ErrNoRows) {
			logErr = nil
		}
		t.logger.Log(QueryLog{
			Query:    query,
			Args:     args,
			Duration: time.Since(start),
			Err:      logErr,
			Context:  ctxToMap(ctx),
		})
	}
	return err
}

func (t *loggedTx) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	start := time.Now()
	res, err := t.inner.Exec(ctx, query, args...)
	var rows int64
	if err == nil && res != nil {
		rows, _ = res.RowsAffected()
	}
	if t.logger != nil {
		t.logger.Log(QueryLog{
			Query:    query,
			Args:     args,
			Duration: time.Since(start),
			Err:      err,
			Rows:     rows,
			Context:  ctxToMap(ctx),
		})
	}
	return res, err
}

func (t *loggedTx) Begin(ctx context.Context) (Tx, error) {
	return nil, errors.New("cannot begin a transaction within a transaction")
}

func (t *loggedTx) Commit() error   { return t.inner.Commit() }
func (t *loggedTx) Rollback() error { return t.inner.Rollback() }

type LoggedExecutor struct {
	inner  Executor
	logger QueryLogger
}

func NewLoggedExecutor(inner Executor, log QueryLogger) *LoggedExecutor {
	return &LoggedExecutor{inner: inner, logger: log}
}

func interpolateQuery(query string, args []any) string {
	if len(args) == 0 {
		return query
	}

	idx := 0
	result := reLogPlaceholder.ReplaceAllStringFunc(query, func(match string) string {
		if strings.HasPrefix(match, "'") {
			return match
		}

		if idx < len(args) {
			arg := args[idx]
			idx++
			return formatArg(arg)
		}
		return match
	})
	return result
}

func formatArg(arg any) string {
	switch v := arg.(type) {
	case string:
		if len(v) > 100 {
			return fmt.Sprintf("'%s...(masked)'", v[:20])
		}
		return fmt.Sprintf("'%s'", strings.ReplaceAll(v, "'", "''"))
	case []byte:
		if len(v) > 100 {
			return fmt.Sprintf("'%x...(masked)'", v[:20])
		}
		return fmt.Sprintf("'%x'", v)
	case time.Time:
		return fmt.Sprintf("'%s'", v.Format("2006-01-02 15:04:05"))
	default:
		return fmt.Sprintf("%v", arg)
	}
}

func (e *LoggedExecutor) Select(ctx context.Context, dest any, query string, args ...any) error {
	start := time.Now()
	err := e.inner.Select(ctx, dest, query, args...)
	if e.logger != nil {
		e.logger.Log(QueryLog{
			Query:    query,
			Args:     args,
			Duration: time.Since(start),
			Err:      err,
			Context:  ctxToMap(ctx),
		})
	}
	return err
}

func (e *LoggedExecutor) Get(ctx context.Context, dest any, query string, args ...any) error {
	start := time.Now()
	err := e.inner.Get(ctx, dest, query, args...)

	if e.logger != nil {
		logErr := err
		if errors.Is(err, sql.ErrNoRows) {
			logErr = nil
		}
		e.logger.Log(QueryLog{
			Query:    query,
			Args:     args,
			Duration: time.Since(start),
			Err:      logErr,
			Context:  ctxToMap(ctx),
		})
	}
	return err
}

func (e *LoggedExecutor) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	start := time.Now()
	res, err := e.inner.Exec(ctx, query, args...)
	var rows int64
	if err == nil && res != nil {
		rows, _ = res.RowsAffected()
	}
	if e.logger != nil {
		e.logger.Log(QueryLog{
			Query:    query,
			Args:     args,
			Duration: time.Since(start),
			Err:      err,
			Rows:     rows,
			Context:  ctxToMap(ctx),
		})
	}
	return res, err
}

func (e *LoggedExecutor) Begin(ctx context.Context) (Tx, error) {
	tx, err := e.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &loggedTx{inner: tx, logger: e.logger}, nil
}

func ctxToMap(ctx context.Context) map[string]any {
	m := make(map[string]any)
	if reqID := ctx.Value(ctxkey.RequestID); reqID != nil {
		m["request_id"] = reqID
	}
	return m
}

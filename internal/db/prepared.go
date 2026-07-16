package db

import (
	"context"
	"database/sql"
	"reflect"
	"sync"

	"github.com/jmoiron/sqlx"
)

type preparedCache struct {
	mu    sync.RWMutex
	db    *sqlx.DB
	stmts map[string]*sqlx.Stmt
	max   int
}

func newPreparedCache(sqlxDB *sqlx.DB, max int) *preparedCache {
	if max <= 0 {
		max = 200
	}
	return &preparedCache{db: sqlxDB, stmts: make(map[string]*sqlx.Stmt), max: max}
}

func (c *preparedCache) get(ctx context.Context, query string) (*sqlx.Stmt, error) {
	c.mu.RLock()
	stmt, ok := c.stmts[query]
	c.mu.RUnlock()
	if ok {
		return stmt, nil
	}

	stmt, err := c.db.PreparexContext(ctx, query)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if existing, ok := c.stmts[query]; ok {
		c.mu.Unlock()
		stmt.Close()
		return existing, nil
	}
	if len(c.stmts) >= c.max {
		for k, s := range c.stmts {
			s.Close()
			delete(c.stmts, k)
			break
		}
	}
	c.stmts[query] = stmt
	c.mu.Unlock()

	return stmt, nil
}

func (c *preparedCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, s := range c.stmts {
		s.Close()
		delete(c.stmts, k)
	}
	return nil
}

type preparedExecutor struct {
	inner *sqlxExecutor
	cache *preparedCache
}

func newPreparedExecutor(sqlxDB *sqlx.DB, maxStmts int) *preparedExecutor {
	return &preparedExecutor{
		inner: &sqlxExecutor{db: sqlxDB},
		cache: newPreparedCache(sqlxDB, maxStmts),
	}
}

func (e *preparedExecutor) Select(ctx context.Context, dest any, query string, args ...any) error {
	stmt, err := e.cache.get(ctx, query)
	if err != nil {
		return e.inner.Select(ctx, dest, query, args...)
	}

	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Slice {
		return stmt.SelectContext(ctx, dest, args...)
	}

	rows, err := stmt.QueryxContext(ctx, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	sliceVal := destVal.Elem()
	isPtr := sliceVal.Type().Elem().Kind() == reflect.Ptr
	baseType := sliceVal.Type().Elem()
	if isPtr {
		baseType = baseType.Elem()
	}

	for rows.Next() {
		results := make(map[string]interface{})
		if err := rows.MapScan(results); err != nil {
			return err
		}

		newItem := reflect.New(baseType)
		if err := scanMapToStruct(results, newItem.Interface()); err != nil {
			return err
		}

		if isPtr {
			sliceVal.Set(reflect.Append(sliceVal, newItem))
		} else {
			sliceVal.Set(reflect.Append(sliceVal, newItem.Elem()))
		}
	}

	return rows.Err()
}

func (e *preparedExecutor) Get(ctx context.Context, dest any, query string, args ...any) error {
	stmt, err := e.cache.get(ctx, query)
	if err != nil {
		return e.inner.Get(ctx, dest, query, args...)
	}

	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Struct {
		return stmt.GetContext(ctx, dest, args...)
	}

	rows, err := stmt.QueryxContext(ctx, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		return sql.ErrNoRows
	}

	results := make(map[string]interface{})
	if err := rows.MapScan(results); err != nil {
		return err
	}

	return scanMapToStruct(results, dest)
}

func (e *preparedExecutor) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	stmt, err := e.cache.get(ctx, query)
	if err != nil {
		return e.inner.Exec(ctx, query, args...)
	}
	return stmt.ExecContext(ctx, args...)
}

func (e *preparedExecutor) Begin(ctx context.Context) (Tx, error) {
	return e.inner.Begin(ctx)
}

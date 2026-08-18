package db

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sync/atomic"

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

type sqlxExecutor struct {
	db *sqlx.DB
}

func (e *sqlxExecutor) Select(ctx context.Context, dest any, query string, args ...any) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Slice {
		return e.db.SelectContext(ctx, dest, query, args...)
	}

	rows, err := e.db.QueryxContext(ctx, query, args...)
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

func (e *sqlxExecutor) Get(ctx context.Context, dest any, query string, args ...any) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Struct {
		return e.db.GetContext(ctx, dest, query, args...)
	}

	rows, err := e.db.QueryxContext(ctx, query, args...)
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

func (e *sqlxExecutor) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	return e.db.ExecContext(ctx, query, args...)
}

func (e *sqlxExecutor) Begin(ctx context.Context) (Tx, error) {
	tx, err := e.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlxTx{tx: tx, spCount: new(int32)}, nil
}

type sqlxTx struct {
	tx *sqlx.Tx
	// spCount is shared (by pointer) with every savepoint nested inside this transaction,
	// so SAVEPOINT names stay unique across the whole tree no matter how deep it nests.
	spCount *int32
}

func (t *sqlxTx) Select(ctx context.Context, dest any, query string, args ...any) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Slice {
		return t.tx.SelectContext(ctx, dest, query, args...)
	}

	rows, err := t.tx.QueryxContext(ctx, query, args...)
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

func (t *sqlxTx) Get(ctx context.Context, dest any, query string, args ...any) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Struct {
		return t.tx.GetContext(ctx, dest, query, args...)
	}

	rows, err := t.tx.QueryxContext(ctx, query, args...)
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

func (t *sqlxTx) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

// Begin opens a nested transaction as a SAVEPOINT on the same underlying connection,
// instead of failing - a plain "second BeginTx" is not possible with database/sql, but a
// savepoint gives the same rollback/commit semantics for the caller. Syntax is identical
// on PostgreSQL and MySQL/InnoDB, so no per-dialect branching is needed here.
func (t *sqlxTx) Begin(ctx context.Context) (Tx, error) {
	n := atomic.AddInt32(t.spCount, 1)
	name := fmt.Sprintf("kiya_sp_%d", n)
	if _, err := t.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return nil, err
	}
	return &sqlxSavepoint{sqlxTx: t, name: name}, nil
}

func (t *sqlxTx) Commit() error {
	return t.tx.Commit()
}

func (t *sqlxTx) Rollback() error {
	return t.tx.Rollback()
}

// sqlxSavepoint is a nested transaction: Select/Get/Exec/Begin are inherited unchanged from
// the embedded sqlxTx (a savepoint runs on the exact same *sqlx.Tx/connection as its parent,
// it is not a separate connection), only Commit/Rollback differ - they release or roll back
// to the savepoint instead of ending the whole transaction.
type sqlxSavepoint struct {
	*sqlxTx
	name string
}

func (s *sqlxSavepoint) Commit() error {
	_, err := s.tx.Exec("RELEASE SAVEPOINT " + s.name)
	return err
}

func (s *sqlxSavepoint) Rollback() error {
	_, err := s.tx.Exec("ROLLBACK TO SAVEPOINT " + s.name)
	return err
}

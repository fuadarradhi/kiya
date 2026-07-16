package db

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/fuadarradhi/kiya/internal/httperr"
	"github.com/fuadarradhi/kiya/internal/logger"
)

func nowFunc() time.Time { return time.Now() }

type ActorResolver interface {
	CurrentUser() (id any, name string)
}

func notFoundErr() error {
	return httperr.New(404, "data tidak ditemukan", ErrRecordNotFound)
}

type historyOnlyRow struct {
	History string `db:"history"`
}

func (b *Builder) resolveOperand(args []any) any {
	if len(args) > 0 {
		return args[0]
	}
	return b.dest
}

func structPtr(d any) (any, bool) {
	if d == nil {
		return nil, false
	}
	v := reflect.ValueOf(d)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return nil, false
	}
	if v.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	return d, true
}

func (b *Builder) actor() (any, string) {
	if ar, ok := b.res.(ActorResolver); ok {
		return ar.CurrentUser()
	}
	return nil, ""
}

func (b *Builder) selectedColNames() []string {
	if len(b.selects) == 0 {
		return nil
	}
	cols := make([]string, 0, len(b.selects))
	for _, s := range b.selects {
		cols = append(cols, s.expr)
	}
	return cols
}

func (b *Builder) ensureAuxCol(self any, structField, colName string) {
	if len(b.selects) == 0 {
		return
	}
	if !hasField(self, structField) {
		return
	}
	for _, s := range b.selects {
		if s.expr == colName {
			return
		}
	}
	b.selects = append(b.selects, selectClause{expr: colName})
}

func syncPrimaryKey(self, old any) {
	if id, err := PrimaryKeyValue(old); err == nil {
		_ = SetPrimaryKeyValue(self, id)
	}
}

func (b *Builder) withTx(fn func(tb *Builder) error) error {
	if _, already := b.executor.(Tx); already {
		return fn(b)
	}

	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	tx, err := b.executor.Begin(ctx)
	if err != nil {
		return err
	}

	tb := b.clone()
	tb.executor = tx

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if ferr := fn(tb); ferr != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			logger.LogError("[DB] Transaction rollback error: %v", rbErr)
		}
		return ferr
	}

	return tx.Commit()
}

func (b *Builder) lockCurrentRow(self any) (any, bool, error) {
	old := reflect.New(reflect.TypeOf(self).Elem()).Interface()

	lb := b.clone()
	lb.dest = nil
	lb.selects = nil

	found, err := lb.LockForUpdate().Find(old)
	if err != nil {
		return nil, false, err
	}
	return old, found, nil
}

func (b *Builder) loadRawHistoryColumn(lock bool) (string, bool, error) {
	row := &historyOnlyRow{}

	c := b.clone()
	c.dest = nil
	c.selects = nil
	c.Cols("history")
	if lock {
		c.LockForUpdate()
	}

	found, err := c.Find(row)
	if err != nil {
		return "", false, err
	}
	return row.History, found, nil
}

func historyColumnHint(err error) error {
	return fmt.Errorf("kiya: gagal membaca kolom history (kalau tabel ini tidak punya kolom history, panggil .NoHistory()): %w", err)
}

func (b *Builder) insertWithHistory(self any) error {
	actorID, actorName := b.actor()

	if !b.historyTrackingDisabled {
		changes, err := StructToHistoryMap(self, b.selectedColNames())
		if err != nil {
			return err
		}
		if idx, ok := historyFieldIndex(self); ok {
			newHist := appendHistoryJSON("", buildHistoryEntry("created", changes, actorID, actorName))
			setHistoryString(self, idx, newHist)
			b.ensureAuxCol(self, "History", "history")
		}
	}
	if hasField(self, "CreatedBy") {
		setActorField(self, "CreatedBy", actorName)
		b.ensureAuxCol(self, "CreatedBy", "created_by")
	}

	_, err := b.execInsertRaw(self)
	return err
}

func (b *Builder) updateExistingOrNotFound(self any) error {
	old, found, err := b.lockCurrentRow(self)
	if err != nil {
		return err
	}
	if !found {
		return notFoundErr()
	}

	actorID, actorName := b.actor()

	if !b.historyTrackingDisabled {
		changes, err := diffForHistory(old, self, b.selectedColNames())
		if err != nil {
			return err
		}
		if idx, ok := historyFieldIndex(old); ok && len(changes) > 0 {
			newHist := appendHistoryJSON(getHistoryString(old, idx), buildHistoryEntry("modified", changes, actorID, actorName))
			setHistoryString(self, idx, newHist)
			b.ensureAuxCol(self, "History", "history")
		}
	}
	if hasField(self, "ModifiedBy") {
		setActorField(self, "ModifiedBy", actorName)
		b.ensureAuxCol(self, "ModifiedBy", "modified_by")
	}

	affected, err := b.execUpdate(self, true)
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFoundErr()
	}
	if affected > 1 {
		logger.LogWarn("[DB] matched %d rows on table '%s' — history entry reflects a single locked row; use UpdateAll for intentional bulk updates", affected, b.table)
	}

	syncPrimaryKey(self, old)
	return nil
}

func (b *Builder) updateWithHistory(self any) error {
	b.applyScope()
	if err := b.checkWhereRequired(); err != nil {
		return err
	}

	return b.withTx(func(tb *Builder) error {
		return tb.updateExistingOrNotFound(self)
	})
}

func (b *Builder) upsertWithHistory(self any) error {
	if len(b.wheres) == 0 {
		return b.insertWithHistory(self)
	}

	b.applyScope()
	return b.withTx(func(tb *Builder) error {
		return tb.updateExistingOrNotFound(self)
	})
}

func (b *Builder) deleteWithHistory(self any) error {
	b.applyScope()
	if err := b.checkWhereRequired(); err != nil {
		return err
	}

	return b.withTx(func(tb *Builder) error {
		old, found, err := tb.lockCurrentRow(self)
		if err != nil {
			return err
		}
		if !found {
			return notFoundErr()
		}

		actorID, actorName := tb.actor()

		if tb.softDeleteCondition == "" {
			if _, err := tb.execHardDelete(self); err != nil {
				return err
			}
			syncPrimaryKey(self, old)
			return nil
		}

		data := map[string]any{"deleted_at": nowFunc()}
		if hasField(self, "DeletedBy") {
			data["deleted_by"] = actorName
		}
		if !tb.historyTrackingDisabled {
			if idx, ok := historyFieldIndex(old); ok {
				data["history"] = appendHistoryJSON(getHistoryString(old, idx), buildHistoryEntry("deleted", nil, actorID, actorName))
			}
		}

		tb.selects = nil
		affected, err := tb.execUpdate(data, true)
		if err != nil {
			return err
		}
		if affected == 0 {
			return notFoundErr()
		}

		syncPrimaryKey(self, old)
		return nil
	})
}

func (b *Builder) restoreWithHistory(self any) error {
	b.softDeleteCondition = ""
	b.applyScope()
	if err := b.checkWhereRequired(); err != nil {
		return err
	}

	return b.withTx(func(tb *Builder) error {
		old, found, err := tb.lockCurrentRow(self)
		if err != nil {
			return err
		}
		if !found {
			return notFoundErr()
		}

		actorID, actorName := tb.actor()

		data := map[string]any{"deleted_at": nil}
		if hasField(self, "DeletedBy") {
			data["deleted_by"] = nil
		}
		if !tb.historyTrackingDisabled {
			if idx, ok := historyFieldIndex(old); ok {
				data["history"] = appendHistoryJSON(getHistoryString(old, idx), buildHistoryEntry("restored", nil, actorID, actorName))
			}
		}

		tb.selects = nil
		affected, err := tb.execUpdate(data, true)
		if err != nil {
			return err
		}
		if affected == 0 {
			return notFoundErr()
		}

		syncPrimaryKey(self, old)
		return nil
	})
}

func (b *Builder) purgeWithModel(self any) error {
	b.softDeleteCondition = ""
	b.applyScope()
	if err := b.checkWhereRequired(); err != nil {
		return err
	}

	return b.withTx(func(tb *Builder) error {
		old, found, err := tb.lockCurrentRow(self)
		if err != nil {
			return err
		}
		if !found {
			return notFoundErr()
		}

		if _, err := tb.execHardDelete(self); err != nil {
			return err
		}
		syncPrimaryKey(self, old)
		return nil
	})
}

func (b *Builder) insertMapWithHistory(data map[string]any) error {
	actorID, actorName := b.actor()

	changes := make(map[string]any, len(data))
	for k, v := range data {
		changes[k] = v
	}

	newData := make(map[string]any, len(data)+1)
	for k, v := range data {
		newData[k] = v
	}
	newData["history"] = appendHistoryJSON("", buildHistoryEntry("created", changes, actorID, actorName))

	_, err := b.execInsertRaw(newData)
	return err
}

func (b *Builder) updateMapWithHistory(data map[string]any) error {
	b.applyScope()
	if err := b.checkWhereRequired(); err != nil {
		return err
	}

	return b.withTx(func(tb *Builder) error {
		existingHist, found, err := tb.loadRawHistoryColumn(true)
		if err != nil {
			return historyColumnHint(err)
		}
		if !found {
			return notFoundErr()
		}

		actorID, actorName := tb.actor()

		changes := make(map[string]any, len(data))
		for k, v := range data {
			changes[k] = v
		}

		newData := make(map[string]any, len(data)+1)
		for k, v := range data {
			newData[k] = v
		}
		newData["history"] = appendHistoryJSON(existingHist, buildHistoryEntry("modified", changes, actorID, actorName))

		tb.selects = nil
		affected, err := tb.execUpdate(newData, true)
		if err != nil {
			return err
		}
		if affected == 0 {
			return notFoundErr()
		}
		if affected > 1 {
			logger.LogWarn("[DB] Update() matched %d rows on table '%s' — history entry reflects a single row; use UpdateAll for intentional bulk updates", affected, tb.table)
		}
		return nil
	})
}

func (b *Builder) deleteRawWithHistory(model any) error {
	b.applyScope()
	if err := b.checkWhereRequired(); err != nil {
		return err
	}

	return b.withTx(func(tb *Builder) error {
		if tb.softDeleteCondition == "" {
			exists, err := tb.Exist()
			if err != nil {
				return err
			}
			if !exists {
				return notFoundErr()
			}
			_, err = tb.execHardDelete(model)
			return err
		}

		existingHist, found, err := tb.loadRawHistoryColumn(true)
		if err != nil {
			return historyColumnHint(err)
		}
		if !found {
			return notFoundErr()
		}

		actorID, actorName := tb.actor()
		data := map[string]any{
			"deleted_at": nowFunc(),
			"history":    appendHistoryJSON(existingHist, buildHistoryEntry("deleted", nil, actorID, actorName)),
		}

		tb.selects = nil
		affected, err := tb.execUpdate(data, true)
		if err != nil {
			return err
		}
		if affected == 0 {
			return notFoundErr()
		}
		return nil
	})
}

func (b *Builder) restoreRawWithHistory() error {
	b.softDeleteCondition = ""
	b.applyScope()
	if err := b.checkWhereRequired(); err != nil {
		return err
	}

	return b.withTx(func(tb *Builder) error {
		existingHist, found, err := tb.loadRawHistoryColumn(true)
		if err != nil {
			return historyColumnHint(err)
		}
		if !found {
			return notFoundErr()
		}

		actorID, actorName := tb.actor()
		data := map[string]any{
			"deleted_at": nil,
			"history":    appendHistoryJSON(existingHist, buildHistoryEntry("restored", nil, actorID, actorName)),
		}

		tb.selects = nil
		affected, err := tb.execUpdate(data, true)
		if err != nil {
			return err
		}
		if affected == 0 {
			return notFoundErr()
		}
		return nil
	})
}

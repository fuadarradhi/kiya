package db

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/fuadarradhi/kiya/internal/logger"
)

func (b *Builder) execInsertRaw(d any) (int64, error) {
	var tableName string
	var mapData map[string]any
	var structInfo *dbCachedStruct

	var selectedCols []string
	if len(b.selects) > 0 {
		for _, s := range b.selects {
			selectedCols = append(selectedCols, s.expr)
		}
	}

	val := reflect.ValueOf(d)
	if val.Kind() == reflect.Map {
		var m map[string]any
		switch t := d.(type) {
		case map[string]any:
			m = t
		default:
			return 0, errors.New("unsupported map type")
		}

		mapData = make(map[string]any)
		filterSet := make(map[string]bool)
		if len(selectedCols) > 0 {
			for _, c := range selectedCols {
				filterSet[c] = true
			}
		}

		for k, v := range m {
			if SanitizeIdentifier(k) == "" {
				continue
			}

			if len(filterSet) > 0 && !filterSet[k] {
				continue
			}

			if b.nullableCols != nil && b.nullableCols[k] {
				rv := reflect.ValueOf(v)
				if !rv.IsValid() || isZero(rv) {
					mapData[k] = nil
				} else {
					mapData[k] = v
				}
			} else {
				mapData[k] = v
			}
		}
	} else {
		var err error
		tableName, err = getTableNameFromModel(d)
		if err != nil {
			return 0, err
		}
		mapData, err = structToMap(d, selectedCols)
		if err != nil {
			return 0, err
		}

		v := reflect.ValueOf(d)
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return 0, ErrModelNil
			}
			v = v.Elem()
		}
		if v.Kind() == reflect.Struct {
			info, err := getStructInfo(v.Type())
			if err == nil {
				structInfo = info
			}
		}
	}

	if len(mapData) == 0 {
		return 0, ErrEmptyData
	}

	clone := b.clone()
	clone.action = "insert"
	clone.inserts = mapData

	if structInfo != nil {
		if len(structInfo.primaryKeys) > 0 {
			clone.primaryKeys = structInfo.primaryKeys
		}
		if structInfo.autoIncCol != "" {
			clone.autoIncCol = structInfo.autoIncCol
		}
	}

	if clone.table == "" {
		if tableName == "" {
			return 0, ErrTableRequired
		}
		clone.table = SanitizeIdentifier(tableName)
		if clone.table == "" {
			return 0, ErrInvalidTableName
		}
	}

	query, args := clone.Build()

	if clone.autoIncCol != "" && clone.dialect.Name() == "postgres" {
		var id int64
		err := clone.executor.Get(clone.ctx, &id, query, args...)
		if err != nil {
			return 0, err
		}
		assignAutoIncrementID(d, id)
		return 1, nil
	}

	res, err := clone.executor.Exec(clone.ctx, query, args...)
	if err != nil {
		return 0, err
	}

	if res != nil {
		if id, idErr := res.LastInsertId(); idErr == nil {
			assignAutoIncrementID(d, id)
		}
	}

	return resultAffected(res, nil)
}

func (b *Builder) Insert(data ...any) error {
	d := b.resolveOperand(data)
	if d == nil {
		return ErrEmptyData
	}

	if self, ok := structPtr(d); ok {
		return b.insertWithHistory(self)
	}

	if m, ok := d.(map[string]any); ok && !b.historyTrackingDisabled {
		return b.insertMapWithHistory(m)
	}

	_, err := b.execInsertRaw(d)
	return err
}

func (b *Builder) InsertBatch(data []any) (int64, error) {
	if len(data) == 0 {
		return 0, ErrEmptyData
	}

	first := data[0]
	tableName, err := getTableNameFromModel(first)
	if err != nil {
		return 0, err
	}

	var selectedCols []string
	if len(b.selects) > 0 {
		for _, s := range b.selects {
			selectedCols = append(selectedCols, s.expr)
		}
	}

	mapDataList := make([]map[string]any, 0, len(data))
	for _, item := range data {
		m, err := structToMap(item, selectedCols)
		if err != nil {
			return 0, err
		}
		if len(m) > 0 {
			mapDataList = append(mapDataList, m)
		}
	}

	if len(mapDataList) == 0 {
		return 0, fmt.Errorf("%w after processing", ErrEmptyData)
	}

	var cols []string
	for k := range mapDataList[0] {
		if SanitizeIdentifier(k) != "" {
			cols = append(cols, k)
		}
	}
	sort.Strings(cols)

	var placeholders []string
	var args []any

	for _, m := range mapDataList {
		rowPh := make([]string, len(cols))
		for i, colName := range cols {
			rowPh[i] = "?"
			args = append(args, m[colName])
		}
		placeholders = append(placeholders, "("+strings.Join(rowPh, ", ")+")")
	}

	clone := b.clone()
	clone.action = "insert_batch"
	clone.table = SanitizeIdentifier(tableName)
	clone.insertBatchCols = cols
	clone.insertBatchPlaceholders = placeholders
	clone.insertBatchArgs = args

	v := reflect.ValueOf(first)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		info, err := getStructInfo(v.Type())
		if err == nil {
			if len(info.primaryKeys) > 0 {
				clone.primaryKeys = info.primaryKeys
			}
		}
	}

	query, qArgs := clone.Build()
	return resultAffected(clone.executor.Exec(clone.ctx, query, qArgs...))
}

func (b *Builder) checkWhereRequired() error {
	if len(b.wheres) > 0 {
		return nil
	}
	if b.unsafeAllowEmptyWhere {
		logger.LogWarn("[DB Security] Unsafe() executed without WHERE clause on table '%s'", b.table)
		return nil
	}
	return ErrWhereClauseRequired
}

func (b *Builder) execUpdate(data any, skipWhereCheck bool) (int64, error) {
	b.applyScope()

	if !skipWhereCheck {
		if err := b.checkWhereRequired(); err != nil {
			return 0, err
		}
	}

	var tableName string
	var mapData map[string]any

	var selectedCols []string
	if len(b.selects) > 0 {
		for _, s := range b.selects {
			selectedCols = append(selectedCols, s.expr)
		}
	}

	val := reflect.ValueOf(data)
	if val.Kind() == reflect.Map {
		var m map[string]any
		switch t := data.(type) {
		case map[string]any:
			m = t
		default:
			return 0, errors.New("unsupported map type")
		}

		mapData = make(map[string]any)
		filterSet := make(map[string]bool)
		if len(selectedCols) > 0 {
			for _, c := range selectedCols {
				filterSet[c] = true
			}
		}

		for k, v := range m {
			if SanitizeIdentifier(k) == "" {
				continue
			}

			if len(filterSet) > 0 && !filterSet[k] {
				continue
			}
			if b.nullableCols != nil && b.nullableCols[k] {
				rv := reflect.ValueOf(v)
				if !rv.IsValid() || isZero(rv) {
					mapData[k] = nil
				} else {
					mapData[k] = v
				}
			} else {
				mapData[k] = v
			}
		}
	} else {
		var err error
		tableName, err = getTableNameFromModel(data)
		if err != nil {
			return 0, err
		}
		mapData, err = structToMap(data, selectedCols)
		if err != nil {
			return 0, err
		}
	}

	if len(mapData) == 0 {
		return 0, ErrEmptyData
	}

	clone := b.clone()
	clone.action = "update"
	clone.updates = mapData

	if clone.table == "" {
		if tableName == "" {
			return 0, ErrTableRequired
		}
		clone.table = SanitizeIdentifier(tableName)
		if clone.table == "" {
			return 0, ErrInvalidTableName
		}
	}

	query, args := clone.Build()
	return resultAffected(clone.executor.Exec(clone.ctx, query, args...))
}

func (b *Builder) Update(data ...any) error {
	d := b.resolveOperand(data)
	if d == nil {
		return ErrEmptyData
	}

	if self, ok := structPtr(d); ok {
		return b.updateWithHistory(self)
	}

	if m, ok := d.(map[string]any); ok && !b.historyTrackingDisabled {
		return b.updateMapWithHistory(m)
	}

	_, err := b.execUpdate(d, false)
	return err
}

func (b *Builder) execDelete(model any, skipWhereCheck bool) (int64, error) {
	b.applyScope()

	if b.softDeleteCondition != "" {
		return b.execUpdate(map[string]any{"deleted_at": nowFunc()}, skipWhereCheck)
	}

	if !skipWhereCheck {
		if err := b.checkWhereRequired(); err != nil {
			return 0, err
		}
	}

	clone := b.clone()
	clone.action = "delete"

	if model != nil {
		tblName, err := getTableNameFromModel(model)
		if err != nil {
			return 0, err
		}

		if clone.table == "" {
			clone.table = SanitizeIdentifier(tblName)
			if clone.table == "" {
				return 0, ErrInvalidTableName
			}
		}
	}

	if clone.table == "" {
		return 0, ErrTableRequired
	}

	query, args := clone.Build()
	return resultAffected(clone.executor.Exec(clone.ctx, query, args...))
}

func (b *Builder) execHardDelete(model any) (int64, error) {
	clone := b.clone()
	clone.softDeleteCondition = ""
	clone.action = "delete"

	if model != nil {
		tblName, err := getTableNameFromModel(model)
		if err != nil {
			return 0, err
		}
		if clone.table == "" {
			clone.table = SanitizeIdentifier(tblName)
			if clone.table == "" {
				return 0, ErrInvalidTableName
			}
		}
	}

	if clone.table == "" {
		return 0, ErrTableRequired
	}

	query, args := clone.Build()
	return resultAffected(clone.executor.Exec(clone.ctx, query, args...))
}

func (b *Builder) Delete(models ...any) error {
	d := b.resolveOperand(models)

	if self, ok := structPtr(d); ok {
		return b.deleteWithHistory(self)
	}

	if !b.historyTrackingDisabled {
		return b.deleteRawWithHistory(d)
	}

	_, err := b.execDelete(d, false)
	return err
}

func (b *Builder) Purge(models ...any) error {
	d := b.resolveOperand(models)

	if self, ok := structPtr(d); ok {
		return b.purgeWithModel(self)
	}

	b.softDeleteCondition = ""
	_, err := b.execDelete(d, false)
	return err
}

func (b *Builder) PurgeAll(models ...any) (int64, error) {
	d := b.resolveOperand(models)
	b.softDeleteCondition = ""
	return b.execDelete(d, true)
}

func (b *Builder) Restore(models ...any) error {
	d := b.resolveOperand(models)

	if self, ok := structPtr(d); ok {
		return b.restoreWithHistory(self)
	}

	if !b.historyTrackingDisabled {
		return b.restoreRawWithHistory()
	}

	b.softDeleteCondition = ""
	_, err := b.execUpdate(map[string]any{"deleted_at": nil}, false)
	return err
}

func (b *Builder) Retain(keepIDs []int64, purge bool, where ...WhereFunc) (int64, error) {
	if b.dest == nil {
		return 0, errors.New("kiya: Retain requires a bound model (call Bind() first)")
	}

	pkCol, err := PrimaryKeyColumn(b.dest)
	if err != nil {
		return 0, err
	}

	if len(keepIDs) > 0 {
		ids := make([]any, len(keepIDs))
		for i, id := range keepIDs {
			ids[i] = id
		}
		b.WhereNotIn(pkCol, ids)
	}
	for _, w := range where {
		w(b)
	}

	if purge {
		b.softDeleteCondition = ""
	}

	return b.DeleteAll(b.dest)
}

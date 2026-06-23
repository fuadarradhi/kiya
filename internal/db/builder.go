package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/jmoiron/sqlx"
)

type whereClause struct {
	boolean string
	expr    string
	args    []any
}

type joinClause struct {
	typ   string
	table string
	on    string
}

type selectClause struct {
	expr string
	args []any
}

// Builder is the opaque struct for building SQL queries.
type Builder struct {
	table    string
	selects  []selectClause
	joins    []joinClause
	wheres   []whereClause
	groupBys []string
	havings  []whereClause
	orderBys []string
	limit    *int
	offset   *int
	args     []any

	action  string
	inserts map[string]any
	updates map[string]any

	insertBatchCols         []string
	insertBatchPlaceholders []string
	insertBatchArgs         []any

	nullableCols map[string]bool

	onDuplicateUpdateCols []string

	primaryKeys []string
	autoIncCol  string

	rawQuery  string
	rawArgs   []any
	namedArgs map[string]any

	dialect  Dialect
	executor Executor
	ctx      context.Context

	dest             any
	res              any
	defaultCondition DefaultConditionFunc

	softDeleteCondition  string
	skipDefaultCondition bool
}

func (b *Builder) Bind(dest any) *Builder {
	b.dest = dest
	return b
}

func (b *Builder) SetResources(res any) *Builder {
	b.res = res
	return b
}

func (b *Builder) WithContext(ctx context.Context) *Builder {
	b.ctx = ctx
	return b
}

func (b *Builder) Use(tx Tx) *Builder {
	b.executor = tx
	return b
}

func (b *Builder) WithoutDefaultCondition() *Builder {
	b.skipDefaultCondition = true
	return b
}

func (b *Builder) Cols(cols ...string) *Builder {
	for _, c := range cols {
		b.selects = append(b.selects, selectClause{expr: c})
	}
	return b
}

func (b *Builder) Nullable(cols ...string) *Builder {
	if b.nullableCols == nil {
		b.nullableCols = make(map[string]bool)
	}
	for _, c := range cols {
		b.nullableCols[c] = true
	}
	return b
}

func (b *Builder) Upsert(data any, updateCols ...string) (Result, error) {
	b.onDuplicateUpdateCols = updateCols
	return b.Insert(data)
}

func (b *Builder) WhereEq(col string, val any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s = ?", safeCol), val)
}

func (b *Builder) Where(expr string, args ...any) *Builder {
	b.wheres = append(b.wheres, whereClause{boolean: "AND", expr: expr, args: args})
	return b
}

func (b *Builder) OrWhere(expr string, args ...any) *Builder {
	b.wheres = append(b.wheres, whereClause{boolean: "OR", expr: expr, args: args})
	return b
}

func (b *Builder) WhereIn(col string, vals []any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}

	if len(vals) == 0 {
		return b.Where("1 = 0")
	}

	placeholders := make([]string, len(vals))
	for i := range vals {
		placeholders[i] = "?"
	}
	expr := fmt.Sprintf("%s IN (%s)", safeCol, strings.Join(placeholders, ", "))
	return b.Where(expr, vals...)
}

func (b *Builder) WhereNull(col string) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s IS NULL", safeCol))
}

func (b *Builder) Join(table, on string, typ string) *Builder {
	safeTable := SanitizeIdentifier(table)
	if safeTable == "" {
		return b
	}

	safeOn := SanitizeOnClause(on)

	b.joins = append(b.joins, joinClause{typ: typ, table: safeTable, on: safeOn})
	return b
}

func (b *Builder) LeftJoin(table, on string) *Builder {
	return b.Join(table, on, "LEFT")
}

func (b *Builder) RightJoin(table, on string) *Builder {
	return b.Join(table, on, "RIGHT")
}

func (b *Builder) InnerJoin(table, on string) *Builder {
	return b.Join(table, on, "INNER")
}

func (b *Builder) GroupBy(cols ...string) *Builder {
	for i, c := range cols {
		cols[i] = SanitizeIdentifier(c)
	}
	validCols := make([]string, 0, len(cols))
	for _, c := range cols {
		if c != "" {
			validCols = append(validCols, c)
		}
	}
	b.groupBys = append(b.groupBys, validCols...)
	return b
}

func (b *Builder) Having(expr string, args ...any) *Builder {
	b.havings = append(b.havings, whereClause{boolean: "AND", expr: expr, args: args})
	return b
}

func (b *Builder) OrderBy(expr string) *Builder {
	safeExpr := SanitizeOrderBy(expr)
	if safeExpr != "" {
		b.orderBys = append(b.orderBys, safeExpr)
	}
	return b
}

func (b *Builder) Limit(n int) *Builder {
	b.limit = &n
	return b
}

func (b *Builder) Offset(n int) *Builder {
	b.offset = &n
	return b
}

func (b *Builder) clone() *Builder {
	newB := *b

	if b.selects != nil {
		newB.selects = make([]selectClause, len(b.selects))
		for i, s := range b.selects {
			newB.selects[i].expr = s.expr
			if len(s.args) > 0 {
				newB.selects[i].args = make([]any, len(s.args))
				copy(newB.selects[i].args, s.args)
			}
		}
	}
	if b.wheres != nil {
		newB.wheres = make([]whereClause, len(b.wheres))
		for i, w := range b.wheres {
			newB.wheres[i].boolean = w.boolean
			newB.wheres[i].expr = w.expr
			if len(w.args) > 0 {
				newB.wheres[i].args = make([]any, len(w.args))
				copy(newB.wheres[i].args, w.args)
			}
		}
	}
	if b.joins != nil {
		newB.joins = make([]joinClause, len(b.joins))
		copy(newB.joins, b.joins)
	}
	if b.groupBys != nil {
		newB.groupBys = make([]string, len(b.groupBys))
		copy(newB.groupBys, b.groupBys)
	}
	if b.havings != nil {
		newB.havings = make([]whereClause, len(b.havings))
		for i, h := range b.havings {
			newB.havings[i].boolean = h.boolean
			newB.havings[i].expr = h.expr
			if len(h.args) > 0 {
				newB.havings[i].args = make([]any, len(h.args))
				copy(newB.havings[i].args, h.args)
			}
		}
	}
	if b.orderBys != nil {
		newB.orderBys = make([]string, len(b.orderBys))
		copy(newB.orderBys, b.orderBys)
	}
	if b.args != nil {
		newB.args = make([]any, len(b.args))
		copy(newB.args, b.args)
	}
	if b.nullableCols != nil {
		newB.nullableCols = make(map[string]bool)
		for k, v := range b.nullableCols {
			newB.nullableCols[k] = v
		}
	}
	if b.onDuplicateUpdateCols != nil {
		newB.onDuplicateUpdateCols = make([]string, len(b.onDuplicateUpdateCols))
		copy(newB.onDuplicateUpdateCols, b.onDuplicateUpdateCols)
	}
	if b.primaryKeys != nil {
		newB.primaryKeys = make([]string, len(b.primaryKeys))
		copy(newB.primaryKeys, b.primaryKeys)
	}
	if b.inserts != nil {
		newB.inserts = make(map[string]any, len(b.inserts))
		for k, v := range b.inserts {
			newB.inserts[k] = v
		}
	}
	if b.updates != nil {
		newB.updates = make(map[string]any, len(b.updates))
		for k, v := range b.updates {
			newB.updates[k] = v
		}
	}
	if b.rawArgs != nil {
		newB.rawArgs = make([]any, len(b.rawArgs))
		copy(newB.rawArgs, b.rawArgs)
	}
	if b.namedArgs != nil {
		newB.namedArgs = make(map[string]any, len(b.namedArgs))
		for k, v := range b.namedArgs {
			newB.namedArgs[k] = v
		}
	}
	if b.insertBatchCols != nil {
		newB.insertBatchCols = make([]string, len(b.insertBatchCols))
		copy(newB.insertBatchCols, b.insertBatchCols)
	}
	if b.insertBatchPlaceholders != nil {
		newB.insertBatchPlaceholders = make([]string, len(b.insertBatchPlaceholders))
		copy(newB.insertBatchPlaceholders, b.insertBatchPlaceholders)
	}
	if b.insertBatchArgs != nil {
		newB.insertBatchArgs = make([]any, len(b.insertBatchArgs))
		copy(newB.insertBatchArgs, b.insertBatchArgs)
	}

	if b.dest != nil {
		newB.dest = b.dest
	}

	return &newB
}

func (b *Builder) applyDefaultCondition() {
	if b.defaultCondition == nil || b.res == nil || b.skipDefaultCondition {
		return
	}

	var fields []string
	if len(b.selects) > 0 {
		for _, s := range b.selects {
			fields = append(fields, s.expr)
		}
	} else if b.dest != nil {
		val := reflect.ValueOf(b.dest)
		if val.Kind() == reflect.Ptr {
			if val.IsNil() {
				return
			}
			val = val.Elem()
		}
		if val.Kind() == reflect.Struct {
			typ := val.Type()
			info, err := getStructInfo(typ)
			if err == nil {
				for _, f := range info.fields {
					fields = append(fields, f.name)
				}
			}
		}
	}

	conds := b.defaultCondition(fields, b.res)
	if len(conds) == 0 {
		return
	}

	keys := make([]string, 0, len(conds))
	for k := range conds {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var groupExpr strings.Builder
	var groupArgs []any
	first := true

	for _, k := range keys {
		safeCol := SanitizeIdentifier(k)
		if safeCol == "" {
			continue
		}
		if !first {
			groupExpr.WriteString(" AND ")
		}
		groupExpr.WriteString(fmt.Sprintf("%s = ?", safeCol))
		groupArgs = append(groupArgs, conds[k])
		first = false
	}

	if groupExpr.Len() == 0 {
		return
	}

	// Wrap existing wheres in parentheses to ensure correct SQL precedence
	// e.g., WHERE (custom_where) AND (default_condition)
	if len(b.wheres) > 0 {
		var existingExpr strings.Builder
		var existingArgs []any
		for i, w := range b.wheres {
			if i > 0 {
				existingExpr.WriteString(" " + w.boolean + " ")
			}
			existingExpr.WriteString(w.expr)
			existingArgs = append(existingArgs, w.args...)
		}
		b.wheres = []whereClause{
			{
				boolean: "AND",
				expr:    "(" + existingExpr.String() + ")",
				args:    existingArgs,
			},
			{
				boolean: "AND",
				expr:    "(" + groupExpr.String() + ")",
				args:    groupArgs,
			},
		}
	} else {
		b.wheres = append(b.wheres, whereClause{
			boolean: "AND",
			expr:    "(" + groupExpr.String() + ")",
			args:    groupArgs,
		})
	}
}

func (b *Builder) Insert(data ...any) (Result, error) {
	var d any
	if len(data) > 0 {
		d = data[0]
	} else {
		d = b.dest
	}

	if d == nil {
		return nil, errors.New("insert data cannot be empty")
	}

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
			return nil, errors.New("unsupported map type")
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
			return nil, err
		}
		mapData, err = structToMap(d, selectedCols, b.nullableCols)
		if err != nil {
			return nil, err
		}

		v := reflect.ValueOf(d)
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return nil, errors.New("struct pointer is nil")
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
		return nil, errors.New("insert data cannot be empty")
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
			return nil, errors.New("table name is required")
		}
		clone.table = SanitizeIdentifier(tableName)
		if clone.table == "" {
			return nil, errors.New("invalid table name inferred from model")
		}
	}

	query, args := clone.Build()

	if clone.autoIncCol != "" && clone.dialect.Name() == "postgres" {
		var id int64
		err := clone.executor.Get(clone.ctx, &id, query, args...)
		if err != nil {
			return nil, err
		}
		assignAutoIncrementID(d, id)
		return &insertResult{lastInsertId: id, rowsAffected: 1}, nil
	}

	res, err := clone.executor.Exec(clone.ctx, query, args...)

	if err == nil && res != nil {
		if id, idErr := res.LastInsertId(); idErr == nil {
			assignAutoIncrementID(d, id)
		}
	}

	return res, err
}

// InsertBatch inserts multiple records in a single SQL query.
func (b *Builder) InsertBatch(data []any) (Result, error) {
	if len(data) == 0 {
		return nil, errors.New("insert batch data cannot be empty")
	}

	first := data[0]
	tableName, err := getTableNameFromModel(first)
	if err != nil {
		return nil, err
	}

	var selectedCols []string
	if len(b.selects) > 0 {
		for _, s := range b.selects {
			selectedCols = append(selectedCols, s.expr)
		}
	}

	mapDataList := make([]map[string]any, 0, len(data))
	for _, item := range data {
		m, err := structToMap(item, selectedCols, b.nullableCols)
		if err != nil {
			return nil, err
		}
		if len(m) > 0 {
			mapDataList = append(mapDataList, m)
		}
	}

	if len(mapDataList) == 0 {
		return nil, errors.New("insert batch data cannot be empty after processing")
	}

	// Determine columns from the first map
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

	// Get primary keys for upsert support
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
	res, err := clone.executor.Exec(clone.ctx, query, qArgs...)
	return res, err
}

func (b *Builder) execUpdate(data any, skipWhereCheck bool) (Result, error) {
	b.applyDefaultCondition()

	if !skipWhereCheck && len(b.wheres) == 0 {
		return nil, errors.New("update requires where clause (safety check)")
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
			return nil, errors.New("unsupported map type")
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
			return nil, err
		}
		mapData, err = structToMap(data, selectedCols, b.nullableCols)
		if err != nil {
			return nil, err
		}
	}

	if len(mapData) == 0 {
		return nil, errors.New("update data cannot be empty")
	}

	clone := b.clone()
	clone.action = "update"
	clone.updates = mapData

	if clone.table == "" {
		if tableName == "" {
			return nil, errors.New("table name is required")
		}
		clone.table = SanitizeIdentifier(tableName)
		if clone.table == "" {
			return nil, errors.New("invalid table name inferred from model")
		}
	}

	query, args := clone.Build()
	res, err := clone.executor.Exec(clone.ctx, query, args...)

	return res, err
}

func (b *Builder) Update(data ...any) (Result, error) {
	var d any
	if len(data) > 0 {
		d = data[0]
	} else {
		d = b.dest
	}
	if d == nil {
		return nil, errors.New("update data cannot be empty")
	}
	return b.execUpdate(d, false)
}

func (b *Builder) execDelete(model any, skipWhereCheck bool) (Result, error) {
	b.applyDefaultCondition()

	if b.softDeleteCondition != "" {
		return b.execUpdate(map[string]any{"deleted_at": time.Now()}, skipWhereCheck)
	}

	if !skipWhereCheck && len(b.wheres) == 0 {
		return nil, errors.New("delete requires where clause (safety check)")
	}

	clone := b.clone()
	clone.action = "delete"

	if model != nil {
		tblName, err := getTableNameFromModel(model)
		if err != nil {
			return nil, err
		}

		if clone.table == "" {
			clone.table = SanitizeIdentifier(tblName)
			if clone.table == "" {
				return nil, errors.New("invalid table name inferred from model")
			}
		}
	}

	if clone.table == "" {
		return nil, errors.New("table name is required")
	}

	query, args := clone.Build()
	res, err := clone.executor.Exec(clone.ctx, query, args...)
	return res, err
}

func (b *Builder) Delete(models ...any) (Result, error) {
	var d any
	if len(models) > 0 {
		d = models[0]
	} else {
		d = b.dest
	}

	return b.execDelete(d, false)
}

func (b *Builder) Purge() (Result, error) {
	b.softDeleteCondition = ""
	return b.execDelete(nil, false)
}

func (b *Builder) Find(dest ...any) (bool, error) {
	b.applyDefaultCondition()

	var d any
	if len(dest) > 0 {
		d = dest[0]
	} else {
		d = b.dest
	}

	if d == nil {
		return false, errors.New("Find: destination is nil")
	}

	clone := b.clone()
	clone.Limit(1)

	val := reflect.ValueOf(d)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return false, errors.New("model is nil")
		}
		val = val.Elem()
	}
	typ := val.Type()

	if typ.Kind() == reflect.Slice {
		typ = typ.Elem()
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
	}

	if typ.Kind() == reflect.Struct {
		info, err := getStructInfo(typ)
		if err != nil {
			return false, fmt.Errorf("Find: %v", err)
		}

		if len(clone.selects) == 0 {
			for _, f := range info.fields {
				clone.Cols(f.name)
			}
		}

		if clone.table == "" {
			clone.table = SanitizeIdentifier(info.defaultName)
		}
	} else {
		if clone.table == "" {
			tblName, err := getTableNameFromModel(d)
			if err != nil {
				return false, fmt.Errorf("Find: %v", err)
			}
			clone.table = SanitizeIdentifier(tblName)
		}
	}

	if clone.table == "" {
		return false, errors.New("Find: table name is required but empty")
	}

	query, args := clone.Build()
	err := clone.executor.Get(clone.ctx, d, query, args...)

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (b *Builder) FindAll(dest ...any) error {
	b.applyDefaultCondition()

	var d any
	if len(dest) > 0 {
		d = dest[0]
	} else {
		d = b.dest
	}

	if d == nil {
		return errors.New("FindAll: destination is nil")
	}

	clone := b.clone()

	sliceVal := reflect.ValueOf(d)
	if sliceVal.Kind() != reflect.Ptr || sliceVal.Elem().Kind() != reflect.Slice {
		if clone.table == "" {
			tblName, err := getTableNameFromModel(d)
			if err != nil {
				return fmt.Errorf("FindAll: %v", err)
			}
			clone.table = SanitizeIdentifier(tblName)
		}
	} else {
		typ := sliceVal.Elem().Type().Elem()
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}

		if typ.Kind() == reflect.Struct {
			info, err := getStructInfo(typ)
			if err != nil {
				return fmt.Errorf("FindAll: %v", err)
			}

			if len(clone.selects) == 0 {
				for _, f := range info.fields {
					clone.Cols(f.name)
				}
			}

			if clone.table == "" {
				clone.table = SanitizeIdentifier(info.defaultName)
			}
		}
	}

	if clone.table == "" {
		return errors.New("FindAll: table name is required")
	}

	query, args := clone.Build()
	return clone.executor.Select(clone.ctx, d, query, args...)
}

func (b *Builder) Count() (int64, error) {
	b.applyDefaultCondition()

	clone := b.clone()
	clone.selects = nil
	clone.Cols("COUNT(*)")

	if clone.table == "" {
		return 0, errors.New("Count: table name is required")
	}

	query, args := clone.Build()
	var count int64
	err := clone.executor.Get(clone.ctx, &count, query, args...)
	return count, err
}

func (b *Builder) Exist() (bool, error) {
	b.applyDefaultCondition()

	clone := b.clone()
	clone.selects = nil
	clone.Cols("1")
	clone.Limit(1)

	if clone.table == "" {
		return false, errors.New("Exist: table name is required")
	}

	query, args := clone.Build()
	var exists int
	err := clone.executor.Get(clone.ctx, &exists, query, args...)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Paginate executes a paginated query and returns pagination metadata.
func (b *Builder) Paginate(dest any, page, perPage int) (*Pagination, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 10
	}

	total, err := b.Count()
	if err != nil {
		return nil, err
	}

	lastPage := 0
	if perPage > 0 {
		lastPage = int((total + int64(perPage) - 1) / int64(perPage))
	}

	p := &Pagination{
		Total:    total,
		Page:     page,
		PerPage:  perPage,
		LastPage: lastPage,
	}

	if total == 0 {
		return p, nil
	}

	err = b.Limit(perPage).Offset((page - 1) * perPage).FindAll(dest)
	if err != nil {
		return nil, err
	}

	return p, nil
}

func (b *Builder) Exec() (Result, error) {
	clone := b.clone()
	query, args := clone.Build()
	res, err := clone.executor.Exec(clone.ctx, query, args...)
	return res, err
}

func (b *Builder) Debug() (string, []any) {
	return b.Build()
}

func (b *Builder) DebugSQL() string {
	q, args := b.Build()
	return interpolateQuery(q, args)
}

func (b *Builder) ToSQL() string {
	q, _ := b.Build()
	return q
}

func (b *Builder) Build() (string, []any) {
	raw, args := b.buildRaw()
	return b.dialect.ConvertPlaceholders(raw), args
}

func (b *Builder) buildRaw() (string, []any) {
	if b.namedArgs != nil && b.rawQuery != "" {
		query, args, err := sqlx.Named(b.rawQuery, b.namedArgs)
		if err != nil {
			logger.LogError("[DB] Named query error: %v", err)
			return b.rawQuery, nil
		}
		return query, args
	}

	if b.rawQuery != "" {
		return b.rawQuery, b.rawArgs
	}

	b.args = nil
	var sql strings.Builder

	switch b.action {
	case "insert":
		b.buildInsert(&sql)
	case "insert_batch":
		b.buildInsertBatch(&sql)
	case "update":
		b.buildUpdate(&sql)
	case "delete":
		b.buildDelete(&sql)
	default:
		b.buildSelect(&sql)
	}

	return sql.String(), b.args
}

func (b *Builder) buildSelect(sql *strings.Builder) {
	sql.WriteString("SELECT ")
	if len(b.selects) > 0 {
		for i, s := range b.selects {
			if i > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(s.expr)
			if len(s.args) > 0 {
				b.args = append(b.args, s.args...)
			}
		}
	} else {
		sql.WriteString("*")
	}

	if b.table == "" {
		sql.WriteString(" FROM undefined_table ")
		logger.LogError("[DB] Build select without table name")
	} else {
		sql.WriteString(" FROM " + b.table)
	}

	for _, j := range b.joins {
		sql.WriteString(fmt.Sprintf(" %s JOIN %s ON %s", j.typ, j.table, j.on))
	}

	b.buildWheres(sql)

	if len(b.groupBys) > 0 {
		sql.WriteString(" GROUP BY " + strings.Join(b.groupBys, ", "))
	}

	if len(b.havings) > 0 {
		sql.WriteString(" HAVING ")
		for i, h := range b.havings {
			if i > 0 {
				sql.WriteString(" " + h.boolean + " ")
			}
			sql.WriteString(h.expr)
			b.args = append(b.args, h.args...)
		}
	}

	if len(b.orderBys) > 0 {
		sql.WriteString(" ORDER BY " + strings.Join(b.orderBys, ", "))
	}

	if b.limit != nil {
		sql.WriteString(" LIMIT ?")
		b.args = append(b.args, *b.limit)
	}

	if b.offset != nil {
		sql.WriteString(" OFFSET ?")
		b.args = append(b.args, *b.offset)
	}
}

func (b *Builder) buildInsert(sql *strings.Builder) {
	var cols []string
	var placeholders []string

	keys := make([]string, 0, len(b.inserts))
	for k := range b.inserts {
		if SanitizeIdentifier(k) == "" {
			logger.LogError("[DB Security] Invalid column name in insert map: %s", k)
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		safeCol := SanitizeIdentifier(k)
		cols = append(cols, safeCol)
		placeholders = append(placeholders, "?")
		b.args = append(b.args, b.inserts[k])
	}

	if len(cols) == 0 {
		logger.LogError("[DB] Build insert with no valid columns")
		return
	}

	sql.WriteString(fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", b.table, strings.Join(cols, ", "), strings.Join(placeholders, ", ")))

	if len(b.onDuplicateUpdateCols) > 0 {
		if b.dialect.Name() == "mysql" {
			var updates []string
			for _, col := range b.onDuplicateUpdateCols {
				safeCol := SanitizeIdentifier(col)
				if safeCol != "" {
					updates = append(updates, fmt.Sprintf("%s = VALUES(%s)", safeCol, safeCol))
				}
			}
			if len(updates) > 0 {
				sql.WriteString(" ON DUPLICATE KEY UPDATE ")
				sql.WriteString(strings.Join(updates, ", "))
			}
		} else if b.dialect.Name() == "postgres" {
			var updates []string
			for _, col := range b.onDuplicateUpdateCols {
				safeCol := SanitizeIdentifier(col)
				if safeCol != "" {
					updates = append(updates, fmt.Sprintf("%s = EXCLUDED.%s", safeCol, safeCol))
				}
			}
			if len(updates) > 0 {
				conflictCols := "id"
				if len(b.primaryKeys) > 0 {
					conflictCols = strings.Join(b.primaryKeys, ", ")
				}
				sql.WriteString(fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET ", conflictCols))
				sql.WriteString(strings.Join(updates, ", "))
			}
		} else {
			logger.LogWarn("[DB] Upsert ON DUPLICATE KEY UPDATE / ON CONFLICT is only supported for MySQL and PostgreSQL dialects.")
		}
	}

	if b.autoIncCol != "" && b.dialect.Name() == "postgres" {
		sql.WriteString(fmt.Sprintf(" RETURNING %s", SanitizeIdentifier(b.autoIncCol)))
	}
}

func (b *Builder) buildInsertBatch(sql *strings.Builder) {
	if len(b.insertBatchCols) == 0 || len(b.insertBatchPlaceholders) == 0 {
		logger.LogError("[DB] Build insert batch with no valid columns or placeholders")
		return
	}

	sql.WriteString(fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		b.table,
		strings.Join(b.insertBatchCols, ", "),
		strings.Join(b.insertBatchPlaceholders, ", "),
	))

	b.args = append(b.args, b.insertBatchArgs...)

	if len(b.onDuplicateUpdateCols) > 0 {
		if b.dialect.Name() == "mysql" {
			var updates []string
			for _, col := range b.onDuplicateUpdateCols {
				safeCol := SanitizeIdentifier(col)
				if safeCol != "" {
					updates = append(updates, fmt.Sprintf("%s = VALUES(%s)", safeCol, safeCol))
				}
			}
			if len(updates) > 0 {
				sql.WriteString(" ON DUPLICATE KEY UPDATE ")
				sql.WriteString(strings.Join(updates, ", "))
			}
		} else if b.dialect.Name() == "postgres" {
			var updates []string
			for _, col := range b.onDuplicateUpdateCols {
				safeCol := SanitizeIdentifier(col)
				if safeCol != "" {
					updates = append(updates, fmt.Sprintf("%s = EXCLUDED.%s", safeCol, safeCol))
				}
			}
			if len(updates) > 0 {
				conflictCols := "id"
				if len(b.primaryKeys) > 0 {
					conflictCols = strings.Join(b.primaryKeys, ", ")
				}
				sql.WriteString(fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET ", conflictCols))
				sql.WriteString(strings.Join(updates, ", "))
			}
		}
	}
}

func (b *Builder) buildUpdate(sql *strings.Builder) {
	var sets []string

	keys := make([]string, 0, len(b.updates))
	for k := range b.updates {
		if SanitizeIdentifier(k) == "" {
			logger.LogError("[DB Security] Invalid column name in update map: %s", k)
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		safeCol := SanitizeIdentifier(k)
		sets = append(sets, fmt.Sprintf("%s = ?", safeCol))
		b.args = append(b.args, b.updates[k])
	}

	if len(sets) == 0 {
		logger.LogError("[DB] Build update with no valid columns")
		return
	}

	sql.WriteString(fmt.Sprintf("UPDATE %s SET %s", b.table, strings.Join(sets, ", ")))
	b.buildWheres(sql)
}

func (b *Builder) buildDelete(sql *strings.Builder) {
	sql.WriteString("DELETE FROM " + b.table)
	b.buildWheres(sql)
}

func (b *Builder) buildWheres(sql *strings.Builder) {
	if len(b.wheres) == 0 && b.softDeleteCondition == "" {
		return
	}

	sql.WriteString(" WHERE ")

	if b.softDeleteCondition != "" {
		sql.WriteString(b.softDeleteCondition)

		if len(b.wheres) > 0 {
			hasOR := false
			for _, w := range b.wheres {
				if w.boolean == "OR" {
					hasOR = true
					break
				}
			}

			if hasOR {
				sql.WriteString(" AND (")
				for i, w := range b.wheres {
					if i > 0 {
						sql.WriteString(" " + w.boolean + " ")
					}
					sql.WriteString(w.expr)
					b.args = append(b.args, w.args...)
				}
				sql.WriteString(")")
			} else {
				for _, w := range b.wheres {
					sql.WriteString(" AND ")
					sql.WriteString(w.expr)
					b.args = append(b.args, w.args...)
				}
			}
		}
	} else {
		for i, w := range b.wheres {
			if i > 0 {
				sql.WriteString(" " + w.boolean + " ")
			}
			sql.WriteString(w.expr)
			b.args = append(b.args, w.args...)
		}
	}
}

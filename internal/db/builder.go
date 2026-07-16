package db

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type WhereFunc func(*Builder)

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

	dest  any
	res   any
	scope ScopeFunc

	softDeleteCondition string

	scopeSkipped bool
	scopeApplied bool

	unsafeAllowEmptyWhere   bool
	lockForUpdate           bool
	historyTrackingDisabled bool
}

func resultAffected(res Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	if res == nil {
		return 0, nil
	}
	n, _ := res.RowsAffected()
	return n, nil
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

func (b *Builder) NoScope() *Builder {
	b.scopeSkipped = true
	return b
}

func (b *Builder) Unsafe() *Builder {
	b.unsafeAllowEmptyWhere = true
	return b
}

func (b *Builder) NoHistory() *Builder {
	b.historyTrackingDisabled = true
	return b
}

func (b *Builder) LockForUpdate() *Builder {
	b.lockForUpdate = true
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

func (b *Builder) OnConflictUpdate(cols ...string) *Builder {
	b.onDuplicateUpdateCols = cols
	return b
}

func (b *Builder) Upsert(data ...any) error {
	d := b.resolveOperand(data)
	if d == nil {
		return ErrEmptyData
	}

	if self, ok := structPtr(d); ok {
		return b.upsertWithHistory(self)
	}

	return b.upsertRawConstraint(d)
}

func (b *Builder) upsertRawConstraint(d any) error {
	if len(b.onDuplicateUpdateCols) == 0 {
		if len(b.selects) > 0 {
			for _, s := range b.selects {
				b.onDuplicateUpdateCols = append(b.onDuplicateUpdateCols, s.expr)
			}
		} else {
			cols, err := columnsForUpsert(d)
			if err != nil {
				return err
			}
			b.onDuplicateUpdateCols = cols
		}
	}

	_, err := b.execInsertRaw(d)
	return err
}

func (b *Builder) WhereEq(col string, val any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s = ?", safeCol), val)
}

func (b *Builder) WhereNotEq(col string, val any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s != ?", safeCol), val)
}

func (b *Builder) WhereGt(col string, val any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s > ?", safeCol), val)
}

func (b *Builder) WhereGte(col string, val any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s >= ?", safeCol), val)
}

func (b *Builder) WhereLt(col string, val any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s < ?", safeCol), val)
}

func (b *Builder) WhereLte(col string, val any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s <= ?", safeCol), val)
}

func (b *Builder) WhereLike(col string, pattern string) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s LIKE ?", safeCol), pattern)
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

func (b *Builder) WhereNotIn(col string, vals []any) *Builder {
	safeCol := SanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}

	if len(vals) == 0 {
		return b
	}

	placeholders := make([]string, len(vals))
	for i := range vals {
		placeholders[i] = "?"
	}
	expr := fmt.Sprintf("%s NOT IN (%s)", safeCol, strings.Join(placeholders, ", "))
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

func (b *Builder) applyScope() {
	if b.scopeApplied {
		return
	}
	b.scopeApplied = true

	if b.scope == nil || b.res == nil || b.scopeSkipped {
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

	conds := b.scope(fields, b.res)
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

	if len(b.wheres) > 0 {
		var existingExpr strings.Builder
		var existingArgs []any
		for i, w := range b.wheres {
			if i > 0 {
				existingExpr.WriteString(" ")
				existingExpr.WriteString(w.boolean)
				existingExpr.WriteString(" ")
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

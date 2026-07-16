package db

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/jmoiron/sqlx"
)

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
		sql.WriteString(" FROM ")
		sql.WriteString(b.table)
	}

	for _, j := range b.joins {
		sql.WriteString(fmt.Sprintf(" %s JOIN %s ON %s", j.typ, j.table, j.on))
	}

	b.buildWheres(sql)

	if len(b.groupBys) > 0 {
		sql.WriteString(" GROUP BY ")
		sql.WriteString(strings.Join(b.groupBys, ", "))
	}

	if len(b.havings) > 0 {
		sql.WriteString(" HAVING ")
		for i, h := range b.havings {
			if i > 0 {
				sql.WriteString(" ")
				sql.WriteString(h.boolean)
				sql.WriteString(" ")
			}
			sql.WriteString(h.expr)
			b.args = append(b.args, h.args...)
		}
	}

	if len(b.orderBys) > 0 {
		sql.WriteString(" ORDER BY ")
		sql.WriteString(strings.Join(b.orderBys, ", "))
	}

	if b.limit != nil {
		sql.WriteString(" LIMIT ?")
		b.args = append(b.args, *b.limit)
	}

	if b.offset != nil {
		sql.WriteString(" OFFSET ?")
		b.args = append(b.args, *b.offset)
	}

	if b.lockForUpdate {
		sql.WriteString(" FOR UPDATE")
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
	sql.WriteString("DELETE FROM ")
	sql.WriteString(b.table)
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
						sql.WriteString(" ")
						sql.WriteString(w.boolean)
						sql.WriteString(" ")
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
				sql.WriteString(" ")
				sql.WriteString(w.boolean)
				sql.WriteString(" ")
			}
			sql.WriteString(w.expr)
			b.args = append(b.args, w.args...)
		}
	}
}

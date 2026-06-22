package kiya

import (
	"fmt"
	"sort"
	"strings"
)

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
		LogError("[DB] Build select without table name")
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
		if sanitizeIdentifier(k) == "" {
			LogError("[DB Security] Invalid column name in insert map: %s", k)
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		safeCol := sanitizeIdentifier(k)
		cols = append(cols, safeCol)
		placeholders = append(placeholders, "?")
		b.args = append(b.args, b.inserts[k])
	}

	if len(cols) == 0 {
		LogError("[DB] Build insert with no valid columns")
		return
	}

	sql.WriteString(fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", b.table, strings.Join(cols, ", "), strings.Join(placeholders, ", ")))

	if len(b.onDuplicateUpdateCols) > 0 {
		if b.dialect.Name() == "mysql" {
			var updates []string
			for _, col := range b.onDuplicateUpdateCols {
				safeCol := sanitizeIdentifier(col)
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
				safeCol := sanitizeIdentifier(col)
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
			LogWarn("[DB] Upsert ON DUPLICATE KEY UPDATE / ON CONFLICT is only supported for MySQL and PostgreSQL dialects.")
		}
	}

	if b.autoIncCol != "" && b.dialect.Name() == "postgres" {
		sql.WriteString(fmt.Sprintf(" RETURNING %s", sanitizeIdentifier(b.autoIncCol)))
	}
}

func (b *Builder) buildUpdate(sql *strings.Builder) {
	var sets []string

	keys := make([]string, 0, len(b.updates))
	for k := range b.updates {
		if sanitizeIdentifier(k) == "" {
			LogError("[DB Security] Invalid column name in update map: %s", k)
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		safeCol := sanitizeIdentifier(k)
		sets = append(sets, fmt.Sprintf("%s = ?", safeCol))
		b.args = append(b.args, b.updates[k])
	}

	if len(sets) == 0 {
		LogError("[DB] Build update with no valid columns")
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
			hasOr := false
			for _, w := range b.wheres {
				if w.boolean == "OR" {
					hasOr = true
					break
				}
			}

			if hasOr {
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

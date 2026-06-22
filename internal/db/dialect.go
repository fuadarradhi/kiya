package db

import (
	"fmt"
	"regexp"
	"strings"
)

var rePostgresPlaceholder = regexp.MustCompile(`('(?:[^']|'')*')|(\$\$.*?\$\$)|(\?)`)

// Dialect interface for SQL query generation specific to database driver.
type Dialect interface {
	Name() string
	ConvertPlaceholders(query string) string
}

// MySQLDialect implementation
type MySQLDialect struct{}

// Name returns the dialect name.
func (d MySQLDialect) Name() string { return "mysql" }

// ConvertPlaceholders for MySQL does nothing as it natively uses `?`.
func (d MySQLDialect) ConvertPlaceholders(query string) string {
	return query
}

// PostgresDialect implementation
type PostgresDialect struct{}

// Name returns the dialect name.
func (d PostgresDialect) Name() string { return "postgres" }

// ConvertPlaceholders changes `?` to `$1, $2, ...` for Postgres while ignoring string literals.
func (d PostgresDialect) ConvertPlaceholders(query string) string {
	i := 0
	return rePostgresPlaceholder.ReplaceAllStringFunc(query, func(match string) string {
		if strings.HasPrefix(match, "'") || strings.HasPrefix(match, "$") {
			return match
		}
		i++
		return fmt.Sprintf("$%d", i)
	})
}

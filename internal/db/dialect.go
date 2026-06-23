package db

import (
	"fmt"
	"regexp"
	"strings"
)

var rePostgresPlaceholder = regexp.MustCompile(`('(?:[^']|'')*')|(\$\$.*?\$\$)|(\?)`)

type Dialect interface {
	Name() string
	ConvertPlaceholders(query string) string
}

type MySQLDialect struct{}

func (d MySQLDialect) Name() string { return "mysql" }

func (d MySQLDialect) ConvertPlaceholders(query string) string {
	return query
}

type PostgresDialect struct{}

func (d PostgresDialect) Name() string { return "postgres" }

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

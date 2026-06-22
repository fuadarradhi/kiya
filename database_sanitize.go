package kiya

import (
	"regexp"
	"strings"
)

var (
	reValidIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_\.]+$`)
	reValidOrderBy    = regexp.MustCompile(`^[a-zA-Z0-9_\.\s,]+$`)
)

func sanitizeIdentifier(ident string) string {
	if ident == "" {
		return ""
	}
	if reValidIdentifier.MatchString(ident) {
		return ident
	}
	LogError("[DB Security] Potential SQL Injection blocked in identifier: %s", ident)
	return ""
}

func sanitizeOrderBy(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ""
	}
	lowerExpr := strings.ToLower(expr)
	if strings.Contains(lowerExpr, "select") || strings.Contains(lowerExpr, "case") || strings.Contains(lowerExpr, "when") {
		LogError("[DB Security] Suspicious keywords in OrderBy blocked: %s", expr)
		return ""
	}

	if reValidOrderBy.MatchString(expr) {
		return expr
	}
	LogError("[DB Security] Invalid OrderBy expression blocked: %s", expr)
	return ""
}

func sanitizeOnClause(on string) string {
	onLower := strings.ToLower(on)
	if strings.Contains(on, "--") || strings.Contains(on, "/*") {
		LogError("[DB Security] Suspicious ON clause blocked (comment): %s", on)
		return "1=0"
	}
	if strings.Contains(on, ";") {
		LogError("[DB Security] Suspicious ON clause blocked (semicolon): %s", on)
		return "1=0"
	}
	if strings.Contains(on, "(") || strings.Contains(on, ")") {
		LogError("[DB Security] Suspicious ON clause blocked (parentheses): %s", on)
		return "1=0"
	}
	if strings.Contains(onLower, "select ") || strings.Contains(onLower, " union ") {
		LogError("[DB Security] Suspicious ON clause blocked (keyword): %s", on)
		return "1=0"
	}

	return on
}

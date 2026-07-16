// Package ctxkey holds context.Context keys that need to be shared across
// kiya's internal packages (e.g. request ID, used both by the router and by
// internal/db's query logger). Keeping the key type in one neutral package
// (instead of redeclaring an identically-named-but-different-typed key in
// each package) avoids the classic Go gotcha where two `type ctxKey string`
// declarations in different packages never compare equal, so
// ctx.Value(pkgA.Key) silently returns nil even though the value was set
// with pkgB.Key.
package ctxkey

type Key string

const (
	RequestID Key = "request_id"
)

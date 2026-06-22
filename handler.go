package kiya

// HandlerFunc defines the handler signature for Kiya routes.
type HandlerFunc func(res *Resources) error

// Middleware defines the middleware signature.
type Middleware func(HandlerFunc) HandlerFunc

// GroupFunc defines the signature for route grouping.
type GroupFunc func(r *Router)

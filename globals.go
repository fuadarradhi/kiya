package kiya

import (
	"strconv"
	"sync"
	"time"
)

// Globals provides a concurrency-safe key-value store for global state.
type Globals struct {
	store map[string]any
	mu    sync.RWMutex
}

// Set stores a value for the given key.
func (g *Globals) Set(key string, value any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.store[key] = value
}

// Get retrieves a value for the given key.
func (g *Globals) Get(key string) any {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key]; ok {
		return val
	}
	return nil
}

// Has checks if a key exists in the store.
func (g *Globals) Has(key string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.store[key]
	return ok
}

// Del deletes a key from the store.
func (g *Globals) Del(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.store, key)
}

// Clear removes all keys from the store.
func (g *Globals) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.store = make(map[string]any)
}

// GetString retrieves a value as a string.
func (g *Globals) GetString(key string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key].(string); ok {
		return val
	}
	return ""
}

// GetInt retrieves a value as an int.
func (g *Globals) GetInt(key string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch v := g.store[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		i, _ := strconv.Atoi(v)
		return i
	}
	return 0
}

// GetInt64 retrieves a value as an int64.
func (g *Globals) GetInt64(key string) int64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch v := g.store[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		i, _ := strconv.ParseInt(v, 10, 64)
		return i
	}
	return 0
}

// GetFloat64 retrieves a value as a float64.
func (g *Globals) GetFloat64(key string) float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch v := g.store[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// GetBool retrieves a value as a bool.
func (g *Globals) GetBool(key string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch v := g.store[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	case int:
		return v != 0
	case float64:
		return v != 0
	}
	return false
}

// GetTime retrieves a value as a time.Time.
func (g *Globals) GetTime(key string) time.Time {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key].(time.Time); ok {
		return val
	}
	return time.Time{}
}

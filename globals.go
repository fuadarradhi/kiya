package kiya

import (
	"strconv"
	"sync"
	"time"
)

type Locals struct {
	store map[string]any
	mu    sync.RWMutex
}

func (g *Locals) Set(key string, value any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.store[key] = value
}

func (g *Locals) Get(key string) any {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key]; ok {
		return val
	}
	return nil
}

func (g *Locals) Has(key string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.store[key]
	return ok
}

func (g *Locals) Del(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.store, key)
}

func (g *Locals) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.store = make(map[string]any)
}

func (g *Locals) GetString(key string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key].(string); ok {
		return val
	}
	return ""
}

func (g *Locals) GetInt(key string) int {
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

func (g *Locals) GetInt64(key string) int64 {
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

func (g *Locals) GetFloat64(key string) float64 {
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

func (g *Locals) GetBool(key string) bool {
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

func (g *Locals) GetTime(key string) time.Time {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key].(time.Time); ok {
		return val
	}
	return time.Time{}
}

package security

import (
	"context"
	"hash/fnv"
	"sync"
	"time"
)

const (
	shardCount         = 32
	maxEntriesPerShard = 10000
)

type rateLimiter struct {
	tokens float64
	last   time.Time
}

type rateLimitShard struct {
	mu       sync.Mutex
	limiters map[string]*rateLimiter
}

type Store struct {
	shards          [shardCount]*rateLimitShard
	rate            float64
	burst           float64
	ttl             time.Duration
	cleanupInterval time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
}

func NewStore(rate float64, burst int, ttl time.Duration, cleanupInterval time.Duration) *Store {
	ctx, cancel := context.WithCancel(context.Background())

	if cleanupInterval <= 0 {
		cleanupInterval = 5 * time.Minute
	}

	s := &Store{
		rate:            rate,
		burst:           float64(burst),
		ttl:             ttl,
		cleanupInterval: cleanupInterval,
		ctx:             ctx,
		cancel:          cancel,
	}

	for i := 0; i < shardCount; i++ {
		s.shards[i] = &rateLimitShard{
			limiters: make(map[string]*rateLimiter),
		}
	}

	go s.cleanup()
	return s
}

func (s *Store) Stop() {
	s.cancel()
}

func (s *Store) getShard(key string) *rateLimitShard {
	h := fnv.New32a()
	h.Write([]byte(key))
	return s.shards[h.Sum32()%shardCount]
}

func (s *Store) Allow(key string) bool {
	now := time.Now()
	shard := s.getShard(key)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	lim, ok := shard.limiters[key]
	if !ok {
		if len(shard.limiters) >= maxEntriesPerShard {
			s.evictOldest(shard)
		}

		shard.limiters[key] = &rateLimiter{
			tokens: s.burst - 1,
			last:   now,
		}
		return true
	}

	elapsed := now.Sub(lim.last).Seconds()

	if elapsed < 0 {
		elapsed = 0
	}

	lim.tokens += elapsed * s.rate
	if lim.tokens > s.burst {
		lim.tokens = s.burst
	}
	lim.last = now

	if lim.tokens < 1 {
		return false
	}

	lim.tokens--
	return true
}

func (s *Store) evictOldest(shard *rateLimitShard) {
	deleteCount := maxEntriesPerShard / 10
	if deleteCount < 1 {
		deleteCount = 1
	}

	type kv struct {
		key  string
		last time.Time
	}

	entries := make([]kv, 0, len(shard.limiters))
	for k, lim := range shard.limiters {
		entries = append(entries, kv{key: k, last: lim.last})
	}

	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].last.Before(entries[i].last) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	for i := 0; i < deleteCount && i < len(entries); i++ {
		delete(shard.limiters, entries[i].key)
	}
}

func (s *Store) cleanup() {
	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			for _, shard := range s.shards {
				shard.mu.Lock()
				for k, lim := range shard.limiters {
					if now.Sub(lim.last) > s.ttl {
						delete(shard.limiters, k)
					}
				}
				shard.mu.Unlock()
			}
		}
	}
}

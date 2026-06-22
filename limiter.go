package kiya

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

type rateLimitStore struct {
	shards          [shardCount]*rateLimitShard
	rate            float64
	burst           float64
	ttl             time.Duration
	cleanupInterval time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
}

func newRateLimitStore(rate float64, burst int, ttl time.Duration, cleanupInterval time.Duration) *rateLimitStore {
	ctx, cancel := context.WithCancel(context.Background())

	if cleanupInterval <= 0 {
		cleanupInterval = 5 * time.Minute
	}

	s := &rateLimitStore{
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

func (s *rateLimitStore) Stop() {
	s.cancel()
}

func (s *rateLimitStore) getShard(key string) *rateLimitShard {
	h := fnv.New32a()
	h.Write([]byte(key))
	return s.shards[h.Sum32()%shardCount]
}

func (s *rateLimitStore) allow(key string) bool {
	now := time.Now()
	shard := s.getShard(key)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	lim, ok := shard.limiters[key]
	if !ok {
		if len(shard.limiters) >= maxEntriesPerShard {
			deleteCount := maxEntriesPerShard / 10
			if deleteCount < 1 {
				deleteCount = 1
			}

			i := 0
			for k := range shard.limiters {
				delete(shard.limiters, k)
				i++
				if i >= deleteCount {
					break
				}
			}
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

func (s *rateLimitStore) cleanup() {
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

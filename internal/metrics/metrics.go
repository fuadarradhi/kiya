package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type counterKey struct {
	method      string
	statusClass string
}

type counterEntry struct {
	mu      sync.Mutex
	count   uint64
	seconds float64
	buckets []uint64
}

type Collector struct {
	mu          sync.Mutex
	counters    map[counterKey]*counterEntry
	inFlight    int64
	rateLimited uint64
}

func New() *Collector {
	return &Collector{
		counters: make(map[counterKey]*counterEntry),
	}
}

func (c *Collector) StartRequest() { atomic.AddInt64(&c.inFlight, 1) }
func (c *Collector) EndRequest()   { atomic.AddInt64(&c.inFlight, -1) }

func (c *Collector) IncRateLimited() { atomic.AddUint64(&c.rateLimited, 1) }

func (c *Collector) Observe(method string, status int, d time.Duration) {
	if c == nil {
		return
	}

	key := counterKey{method: strings.ToUpper(method), statusClass: statusClass(status)}

	c.mu.Lock()
	entry, ok := c.counters[key]
	if !ok {
		entry = &counterEntry{buckets: make([]uint64, len(durationBuckets)+1)}
		c.counters[key] = entry
	}
	c.mu.Unlock()

	seconds := d.Seconds()

	entry.mu.Lock()
	entry.count++
	entry.seconds += seconds
	for i, boundary := range durationBuckets {
		if seconds <= boundary {
			entry.buckets[i]++
		}
	}
	entry.buckets[len(durationBuckets)]++
	entry.mu.Unlock()
}

func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return "other"
	}
}

func (c *Collector) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		var b strings.Builder

		fmt.Fprint(&b, "# HELP kiya_http_requests_in_flight Requests currently being served.\n")
		fmt.Fprint(&b, "# TYPE kiya_http_requests_in_flight gauge\n")
		fmt.Fprintf(&b, "kiya_http_requests_in_flight %d\n", atomic.LoadInt64(&c.inFlight))

		fmt.Fprint(&b, "# HELP kiya_http_requests_rate_limited_total Requests rejected by the rate limiter (HTTP 429).\n")
		fmt.Fprint(&b, "# TYPE kiya_http_requests_rate_limited_total counter\n")
		fmt.Fprintf(&b, "kiya_http_requests_rate_limited_total %d\n", atomic.LoadUint64(&c.rateLimited))

		c.mu.Lock()
		keys := make([]counterKey, 0, len(c.counters))
		for k := range c.counters {
			keys = append(keys, k)
		}
		c.mu.Unlock()

		sort.Slice(keys, func(i, j int) bool {
			if keys[i].method != keys[j].method {
				return keys[i].method < keys[j].method
			}
			return keys[i].statusClass < keys[j].statusClass
		})

		fmt.Fprint(&b, "# HELP kiya_http_requests_total Total HTTP requests, labeled by method and status class.\n")
		fmt.Fprint(&b, "# TYPE kiya_http_requests_total counter\n")
		for _, k := range keys {
			e := c.counters[k]
			e.mu.Lock()
			count := e.count
			e.mu.Unlock()
			fmt.Fprintf(&b, "kiya_http_requests_total{method=%q,status=%q} %d\n", k.method, k.statusClass, count)
		}

		fmt.Fprint(&b, "# HELP kiya_http_request_duration_seconds Request duration, labeled by method and status class.\n")
		fmt.Fprint(&b, "# TYPE kiya_http_request_duration_seconds histogram\n")
		for _, k := range keys {
			e := c.counters[k]
			e.mu.Lock()
			buckets := append([]uint64(nil), e.buckets...)
			sum := e.seconds
			count := e.count
			e.mu.Unlock()

			for i, boundary := range durationBuckets {
				le := strconv.FormatFloat(boundary, 'f', -1, 64)
				fmt.Fprintf(&b, "kiya_http_request_duration_seconds_bucket{method=%q,status=%q,le=%q} %d\n",
					k.method, k.statusClass, le, buckets[i])
			}
			fmt.Fprintf(&b, "kiya_http_request_duration_seconds_bucket{method=%q,status=%q,le=\"+Inf\"} %d\n",
				k.method, k.statusClass, buckets[len(durationBuckets)])
			fmt.Fprintf(&b, "kiya_http_request_duration_seconds_sum{method=%q,status=%q} %s\n",
				k.method, k.statusClass, strconv.FormatFloat(sum, 'f', -1, 64))
			fmt.Fprintf(&b, "kiya_http_request_duration_seconds_count{method=%q,status=%q} %d\n",
				k.method, k.statusClass, count)
		}

		w.Write([]byte(b.String()))
	}
}

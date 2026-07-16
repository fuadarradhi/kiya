package db

import "time"

// MetricsSink receives per-query instrumentation events. This is the
// integration point for #4 (observability): kiya does NOT wire any
// implementation by default. If you want query metrics, implement this
// interface (e.g. backed by Prometheus, StatsD, OpenTelemetry, or just an
// in-memory counter for tests) and pass it via
// DatabaseConfig.MetricsSink / db.WithQueryLogger(db.NewMetricsQueryLogger(sink)).
//
// Example Prometheus-ish implementation (illustrative, not included to avoid
// forcing a dependency on everyone):
//
//	type promSink struct {
//	    queryDuration *prometheus.HistogramVec
//	    queryErrors   *prometheus.CounterVec
//	}
//
//	func (p *promSink) ObserveQuery(query string, d time.Duration, err error, rows int64) {
//	    p.queryDuration.WithLabelValues(queryShape(query)).Observe(d.Seconds())
//	    if err != nil {
//	        p.queryErrors.WithLabelValues(queryShape(query)).Inc()
//	    }
//	}
type MetricsSink interface {
	// ObserveQuery is called once per executed query (Select/Get/Exec),
	// after it completes. err is nil on success. sql.ErrNoRows from Get()
	// is NOT reported as an error here (same convention as the existing
	// frameworkLogger), since "not found" is a normal outcome, not a fault.
	ObserveQuery(query string, duration time.Duration, err error, rows int64)
}

// metricsQueryLogger adapts a MetricsSink to the existing QueryLogger
// interface, so it can be combined with the default file/Telegram logger
// via multiLogger without touching LoggedExecutor at all.
type metricsQueryLogger struct {
	sink MetricsSink
}

// NewMetricsQueryLogger wraps sink as a QueryLogger. Pass the result to
// db.WithQueryLogger(...) as an extra option — it is additive, the default
// file/Telegram logger keeps running unaffected.
func NewMetricsQueryLogger(sink MetricsSink) QueryLogger {
	if sink == nil {
		return nil
	}
	return &metricsQueryLogger{sink: sink}
}

func (m *metricsQueryLogger) Log(q QueryLog) {
	if m == nil || m.sink == nil {
		return
	}
	m.sink.ObserveQuery(q.Query, q.Duration, q.Err, q.Rows)
}

// multiLogger fans a single QueryLog out to several QueryLoggers. Used so
// that adding a metrics sink never replaces the default file/Telegram
// logger — both run side by side.
type multiLogger struct {
	loggers []QueryLogger
}

func (m *multiLogger) Log(q QueryLog) {
	for _, l := range m.loggers {
		if l != nil {
			l.Log(q)
		}
	}
}

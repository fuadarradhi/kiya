package db

import "time"

type MetricsSink interface {
	ObserveQuery(query string, duration time.Duration, err error, rows int64)
}

type metricsQueryLogger struct {
	sink MetricsSink
}

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

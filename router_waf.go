package kiya

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/types"
	"github.com/fuadarradhi/kiya/owasp"
)

const defaultMaxWAFBufferSize int64 = 10 << 20

type wafResponseWriter struct {
	http.ResponseWriter
	status              int
	body                bytes.Buffer
	wrote               bool
	streaming           bool
	maxBufferSize       int64
	bufferLimitExceeded bool
	tx                  types.Transaction
	blocked             bool
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rec.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func (rec *statusRecorder) Flush() {
	if fl, ok := rec.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (w *wafResponseWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}

	isBlocked := false
	if w.tx != nil {
		for k, v := range w.Header() {
			lowerKey := strings.ToLower(k)
			for _, vv := range v {
				w.tx.AddResponseHeader(lowerKey, vv)
			}
		}
		w.tx.ProcessResponseHeaders(code, "HTTP/1.1")

		if it := w.tx.Interruption(); it != nil {
			LogWAF("BLOCK Response Header Phase - RuleID: %v", it.RuleID)
			isBlocked = true
			code = it.Status
			w.body.Reset()
			w.body.WriteString("Blocked by WAF")
			w.Header().Set("Content-Type", "text/plain")
		}
	}

	ct := w.Header().Get("Content-Type")

	isBinaryContent := strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "video/") ||
		strings.HasPrefix(ct, "audio/") ||
		ct == "application/octet-stream" ||
		ct == "application/pdf" ||
		strings.HasPrefix(ct, "application/font-") ||
		strings.HasPrefix(ct, "font/")

	if isBinaryContent && !isBlocked {
		w.ResponseWriter.WriteHeader(code)
		w.wrote = true
		w.streaming = true
	} else {
		w.status = code
		w.wrote = true
	}

	if isBlocked {
		w.blocked = true
	}
}

func (w *wafResponseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}

	if w.blocked {
		return len(b), nil
	}

	if w.streaming {
		return w.ResponseWriter.Write(b)
	}

	if w.maxBufferSize > 0 && (int64(w.body.Len())+int64(len(b))) > w.maxBufferSize {
		w.bufferLimitExceeded = true
		w.streaming = true
		w.FlushToClient()
		return w.ResponseWriter.Write(b)
	}

	return w.body.Write(b)
}

func (w *wafResponseWriter) FlushToClient() error {
	if w.streaming {
		return nil
	}

	if w.body.Len() > 0 || w.wrote {
		if !w.wrote {
			w.WriteHeader(http.StatusOK)
		}
		w.ResponseWriter.WriteHeader(w.status)
		_, err := w.ResponseWriter.Write(w.body.Bytes())
		return err
	}
	return nil
}

func (w *wafResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.streaming || w.wrote {
		return nil, nil, fmt.Errorf("cannot hijack after response has been written")
	}

	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		w.streaming = true
		return hj.Hijack()
	}

	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func (w *wafResponseWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wrote {
			w.WriteHeader(w.status)
		}
		if w.streaming {
			fl.Flush()
			return
		}
		w.ResponseWriter.WriteHeader(w.status)
		if w.body.Len() > 0 {
			w.ResponseWriter.Write(w.body.Bytes())
			w.body.Reset()
		}
		w.streaming = true
		fl.Flush()
	}
}

func initWAF(debug bool) (coraza.WAF, error) {
	engineMode := "On"
	if debug {
		engineMode = "DetectionOnly"
	}

	cfg := coraza.NewWAFConfig().
		WithErrorCallback(func(rule types.MatchedRule) {
			r := rule.Rule()

			var matchDetails []string
			for _, md := range rule.MatchedDatas() {
				matchDetails = append(matchDetails, fmt.Sprintf(
					"%v:%s=%s", md.Variable(), md.Key(), md.Value(),
				))
			}

			LogWAF(
				"Matched Rule ID: %d | File: %s | Line: %d | Severity: %s | Matched: [%s] | Raw: %s",
				r.ID(),
				r.File(),
				r.Line(),
				r.Severity().String(),
				strings.Join(matchDetails, "; "),
				r.Raw(),
			)
		})

	directives := fmt.Sprintf(`
        SecRuleEngine %s
        Include crs-setup.conf
        Include rules/*.conf
        Include ignore.conf
    `, engineMode)

	cfg = cfg.WithRootFS(owasp.RulesFS)
	cfg = cfg.WithDirectives(directives)

	wafInstance, err := coraza.NewWAF(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to init WAF: %w", err)
	}

	modeStr := "PROTECTION"
	if debug {
		modeStr = "DETECTION ONLY"
	}
	LogInfo("WAF Initialized successfully (Mode: %s)", modeStr)
	return wafInstance, nil
}

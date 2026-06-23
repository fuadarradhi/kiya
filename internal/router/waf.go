package router

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/types"
	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/fuadarradhi/kiya/internal/util"
	"github.com/fuadarradhi/kiya/owasp"
)

const defaultMaxWAFBufferSize int64 = 10 << 20

type StatusRecorder interface {
	StatusCode() int
}

type WrittenChecker interface {
	Written() bool
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) StatusCode() int {
	return rec.statusCode
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

func NewStatusRecorder(w http.ResponseWriter) http.ResponseWriter {
	return &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
}

type wafResponseWriter struct {
	mu sync.Mutex
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

func (w *wafResponseWriter) StatusCode() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

func (w *wafResponseWriter) Written() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wrote
}

func (w *wafResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()

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
			logger.LogWAF("BLOCK Response Header Phase - RuleID: %v", it.RuleID)
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
	w.mu.Lock()
	defer w.mu.Unlock()

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
	w.mu.Lock()
	defer w.mu.Unlock()

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
	w.mu.Lock()
	defer w.mu.Unlock()

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

func WrapWithWAF(next http.Handler, waf coraza.WAF, maxBufferSize int64) http.Handler {
	if waf == nil {
		return next
	}

	if maxBufferSize <= 0 {
		maxBufferSize = defaultMaxWAFBufferSize
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		wafWriter := &wafResponseWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
			maxBufferSize:  maxBufferSize,
		}

		func() {
			defer func() {
				if err := recover(); err != nil {
					logger.LogError("PANIC recovered (Inside Request Context): %v", err)

					wafWriter.mu.Lock()
					wafWriter.wrote = true
					wafWriter.status = http.StatusInternalServerError
					wafWriter.body.Reset()
					wafWriter.body.WriteString("Internal Server Error")
					wafWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
					wafWriter.mu.Unlock()

					wafWriter.FlushToClient()
				}
			}()

			tx := waf.NewTransaction()
			defer tx.Close()

			wafWriter.mu.Lock()
			wafWriter.tx = tx
			wafWriter.mu.Unlock()

			serverAddr := ""
			if addr := req.Context().Value(http.LocalAddrContextKey); addr != nil {
				if tcpAddr, ok := addr.(net.Addr); ok {
					serverAddr = tcpAddr.String()
				}
			}
			serverIP, serverPort := parseIPPort(serverAddr, 80)

			clientIP := util.RealIP(req)
			clientPort := 0

			tx.ProcessConnection(serverIP, serverPort, clientIP, clientPort)
			tx.ProcessURI(req.RequestURI, req.Method, req.Proto)

			if req.Host != "" {
				tx.AddRequestHeader("host", req.Host)
			}

			for k, v := range req.Header {
				lowerKey := strings.ToLower(k)
				for _, vv := range v {
					tx.AddRequestHeader(lowerKey, vv)
				}
			}

			tx.ProcessRequestHeaders()

			if it := tx.Interruption(); it != nil {
				logger.LogWAF("BLOCK Request Phase - IP: %s | RuleID: %v", clientIP, it.RuleID)
				w.WriteHeader(it.Status)
				w.Write([]byte("Blocked by WAF"))
				return
			}

			var bodyBytes []byte

			if req.Method == http.MethodPost || req.Method == http.MethodPut || req.Method == http.MethodPatch {
				req.Body = http.MaxBytesReader(w, req.Body, maxBufferSize)
				var err error
				bodyBytes, err = io.ReadAll(req.Body)

				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

				if err != nil {
					if strings.Contains(err.Error(), "http: request body too large") {
						w.WriteHeader(http.StatusRequestEntityTooLarge)
						return
					}
					logger.LogError("[WAF] Body read error: %v", err)
					bodyBytes = []byte{}
				}
			}

			tx.WriteRequestBody(bodyBytes)
			tx.ProcessRequestBody()

			if it := tx.Interruption(); it != nil {
				logger.LogWAF("BLOCK Body Phase - IP: %s | RuleID: %v", clientIP, it.RuleID)
				w.WriteHeader(it.Status)
				w.Write([]byte("Blocked by WAF"))
				return
			}

			next.ServeHTTP(wafWriter, req)

			if wafWriter.bufferLimitExceeded {
				logger.LogWarn("WAF: Response body exceeded buffer limit (%d bytes), skipping response body inspection", maxBufferSize)
				return
			}

			if !wafWriter.streaming && wafWriter.body.Len() > 0 {
				tx.WriteResponseBody(wafWriter.body.Bytes())
			} else {
				tx.WriteResponseBody([]byte{})
			}

			tx.ProcessResponseBody()

			if it := wafWriter.tx.Interruption(); it != nil {
				logger.LogWAF("BLOCK Response Body Phase - IP: %s | RuleID: %v", clientIP, it.RuleID)
				if !wafWriter.streaming {
					wafWriter.body.Reset()
					wafWriter.status = it.Status
					wafWriter.body.WriteString("Blocked by WAF (Response Body)")
					wafWriter.Header().Set("Content-Type", "text/plain")
					wafWriter.blocked = true
				} else {
					logger.LogWarn("WARNING: WAF attempted to block a streaming response due to body content. Data might have been sent.")
				}
			}

			if err := wafWriter.FlushToClient(); err != nil {
				logger.LogError("Error flushing response: %v", err)
			}
		}()
	})
}

func InitWAF(debug bool) (coraza.WAF, error) {
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

			logger.LogWAF(
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
	logger.LogInfo("WAF Initialized successfully (Mode: %s)", modeStr)
	return wafInstance, nil
}

func parseIPPort(addr string, defaultPort int) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, defaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, defaultPort
	}
	return host, port
}

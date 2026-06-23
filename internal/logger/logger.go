package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fuadarradhi/kiya/internal/util"
)

// Config holds settings for the logger.
type Config struct {
	Debug   bool
	Token   string
	Group   string
	LogPath string
	WAFPath string
	JSON    bool
}

var (
	consoleLogger *log.Logger

	logFile    *os.File
	currentDay string

	wafLogFile    *os.File
	wafCurrentDay string

	logMutex sync.Mutex

	telegramToken string
	telegramGroup string
	isDebug       bool
	isJSON        bool

	telegramLastSent = make(map[string]time.Time)
	telegramMutex    sync.Mutex
	telegramCancel   context.CancelFunc

	logChan   chan logEntry
	stopLogCh chan struct{}
	logWg     sync.WaitGroup

	logStateMu sync.RWMutex

	telegramSem = make(chan struct{}, 5)
	telegramWg  sync.WaitGroup

	httpClient *http.Client

	droppedLogs atomic.Int64
)

const (
	colorReset   = "\033[0m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorRed     = "\033[31m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
)

type logEntry struct {
	isWAF        bool
	level        string
	message      string
	sendTelegram bool
}

func init() {
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: true,
		},
	}

	consoleLogger = log.New(os.Stdout, "", log.Ldate|log.Ltime)
}

// Init initializes the logger.
func Init(cfg Config) {
	logStateMu.Lock()
	defer logStateMu.Unlock()

	if logChan != nil {
		closeUnsafe()
	}

	isDebug = cfg.Debug
	isJSON = cfg.JSON
	telegramToken = cfg.Token
	telegramGroup = cfg.Group

	consoleLogger = log.New(os.Stdout, "", log.Ldate|log.Ltime)

	if isDebug {
		log.Print("[Kiya] Logger initialized in DEBUG mode (No file logging)")
		return
	}

	logPath := cfg.LogPath
	if logPath == "" {
		logPath = "./temp/log"
	}
	wafPath := cfg.WAFPath
	if wafPath == "" {
		wafPath = "./temp/waf"
	}

	initFileLogger(logPath)
	initWAFFileLogger(wafPath)

	var ctx context.Context
	ctx, telegramCancel = context.WithCancel(context.Background())
	startTelegramCleanup(ctx)

	logChan = make(chan logEntry, 2000)
	stopLogCh = make(chan struct{})

	droppedLogs.Store(0)

	for i := 0; i < 3; i++ {
		logWg.Add(1)
		go logWorker()
	}

	log.Print("[Kiya] Logger initialized in PRODUCTION mode (Async File & Telegram Logging)")
}

func logWorker() {
	defer logWg.Done()
	for {
		select {
		case <-stopLogCh:
			for {
				select {
				case entry := <-logChan:
					processLogEntry(entry)
				default:
					return
				}
			}
		case entry := <-logChan:
			processLogEntry(entry)

			dropped := droppedLogs.Swap(0)
			if dropped > 0 {
				consoleLogger.Printf("%s[WARN]%s %d log entries dropped (channel full)", colorYellow, colorReset, dropped)
			}
		}
	}
}

func processLogEntry(entry logEntry) {
	safeMsg := sanitizeLogMessage(entry.message)

	if entry.isWAF {
		writeWAFToFile(entry.level, safeMsg)
	} else {
		writeAppToFile(entry.level, safeMsg)
	}

	if entry.sendTelegram {
		prefix := "[APP ERROR] "
		if entry.isWAF {
			prefix = "[WAF ATTACK] "
		}
		telegramWg.Add(1)
		go func(msg string) {
			defer telegramWg.Done()
			telegramSem <- struct{}{}
			defer func() { <-telegramSem }()
			sendTelegramDirect(prefix + truncateString(msg, 200))
		}(safeMsg)
	}
}

func sanitizeLogMessage(msg string) string {
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	return msg
}

func formatJSON(level, msg string) string {
	logData := map[string]any{
		"level":   level,
		"message": msg,
		"time":    time.Now().Format(time.RFC3339),
	}
	b, _ := json.Marshal(logData)
	return string(b)
}

func writeAppToFile(level string, msg string) {
	logMutex.Lock()
	defer logMutex.Unlock()

	checkAndRotateApp()

	if logFile != nil {
		var fileLine string
		if isJSON {
			fileLine = formatJSON(level, msg) + "\n"
		} else {
			fileLine = fmt.Sprintf("%s [%s] %s\n",
				time.Now().Format("2006/01/02 15:04:05"),
				level, msg)
		}
		logFile.WriteString(fileLine)
	}
}

func writeWAFToFile(level string, msg string) {
	logMutex.Lock()
	defer logMutex.Unlock()

	checkAndRotateWAF()

	if wafLogFile != nil {
		var fileLine string
		if isJSON {
			fileLine = formatJSON(level, msg) + "\n"
		} else {
			fileLine = fmt.Sprintf("%s [%s] %s\n",
				time.Now().Format("2006/01/02 15:04:05"),
				level, msg)
		}
		wafLogFile.WriteString(fileLine)
	}
}

func startTelegramCleanup(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupTelegramCache()
			}
		}
	}()
}

func cleanupTelegramCache() {
	telegramMutex.Lock()
	defer telegramMutex.Unlock()

	if len(telegramLastSent) > 10000 {
		telegramLastSent = make(map[string]time.Time)
		return
	}

	cutoff := time.Now().Add(-20 * time.Minute)
	for key, t := range telegramLastSent {
		if t.Before(cutoff) {
			delete(telegramLastSent, key)
		}
	}
}

func initFileLogger(logPath string) {
	logMutex.Lock()
	defer logMutex.Unlock()

	os.MkdirAll(logPath, 0755)
	day := time.Now().Format("2006-01-02")
	currentDay = day

	filePath := filepath.Join(logPath, "log-"+day+".log")

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.SetOutput(os.Stderr)
		log.Printf("CRITICAL: Cannot open log file: %v", err)
		return
	}

	logFile = f
}

func initWAFFileLogger(wafPath string) {
	logMutex.Lock()
	defer logMutex.Unlock()

	os.MkdirAll(wafPath, 0755)
	day := time.Now().Format("2006-01-02")
	wafCurrentDay = day

	filePath := filepath.Join(wafPath, "waf-"+day+".log")

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.SetOutput(os.Stderr)
		log.Printf("CRITICAL: Cannot open waf log file: %v", err)
		return
	}

	wafLogFile = f
}

func checkAndRotateApp() {
	if isDebug {
		return
	}

	day := time.Now().Format("2006-01-02")
	if day == currentDay {
		return
	}

	newFilePath := filepath.Join(filepath.Dir(logFile.Name()), "log-"+day+".log")
	f, err := os.OpenFile(newFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}

	if logFile != nil {
		logFile.Close()
	}
	logFile = f
	currentDay = day
}

func checkAndRotateWAF() {
	if isDebug {
		return
	}

	day := time.Now().Format("2006-01-02")
	if day == wafCurrentDay {
		return
	}

	newFilePath := filepath.Join(filepath.Dir(wafLogFile.Name()), "waf-"+day+".log")
	f, err := os.OpenFile(newFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}

	if wafLogFile != nil {
		wafLogFile.Close()
	}
	wafLogFile = f
	wafCurrentDay = day
}

func logf(level string, color string, format string, v ...any) {
	msg := fmt.Sprintf(format, v...)

	logStateMu.RLock()
	cl := consoleLogger
	logStateMu.RUnlock()

	cl.Printf("%s[%s] %s%s", color, level, msg, colorReset)

	logStateMu.RLock()
	ch := logChan
	logStateMu.RUnlock()

	if ch != nil {
		select {
		case ch <- logEntry{
			isWAF:        false,
			level:        level,
			message:      msg,
			sendTelegram: false,
		}:
		default:
			droppedLogs.Add(1)
		}
	}
}

// LogInfo logs an INFO message.
func LogInfo(format string, v ...any) {
	logf("INFO", colorGreen, format, v...)
}

// LogWarn logs a WARN message.
func LogWarn(format string, v ...any) {
	logf("WARN", colorYellow, format, v...)
}

// LogError logs an ERROR message and sends a Telegram alert.
func LogError(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)

	logStateMu.RLock()
	cl := consoleLogger
	debug := isDebug
	ch := logChan
	logStateMu.RUnlock()

	cl.Printf("%s[ERROR] %s%s", colorRed, msg, colorReset)

	if ch != nil {
		select {
		case ch <- logEntry{
			isWAF:        false,
			level:        "ERROR",
			message:      msg,
			sendTelegram: !debug,
		}:
		default:
			droppedLogs.Add(1)
		}
	}
}

// LogWAF logs a WAF/ATTACK message and sends a Telegram alert.
func LogWAF(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)

	logStateMu.RLock()
	debug := isDebug
	cl := consoleLogger
	ch := logChan
	logStateMu.RUnlock()

	if debug {
		fmt.Printf("%s[WAF DEBUG]%s %s\n", colorCyan, colorReset, msg)
		return
	}

	cl.Printf("%s[ATTACK] %s%s", colorMagenta, msg, colorReset)

	if ch != nil {
		select {
		case ch <- logEntry{
			isWAF:        true,
			level:        "ATTACK",
			message:      msg,
			sendTelegram: !debug,
		}:
		default:
			droppedLogs.Add(1)
		}
	}
}

// LogTelegram sends a custom error alert to Telegram with request info.
func LogTelegram(r *http.Request, err any) {
	logStateMu.RLock()
	debug := isDebug
	token := telegramToken
	group := telegramGroup
	logStateMu.RUnlock()

	if debug {
		return
	}

	if err == nil {
		return
	}

	if token == "" || group == "" {
		return
	}

	const maxLen = 2000

	var method, pathStr, ip, query string

	if r != nil {
		method = util.HTMLEscape(r.Method)
		pathStr = util.HTMLEscape(r.URL.Path)
		query = r.URL.RawQuery
		ip = util.HTMLEscape(util.RealIP(r))
	} else {
		method = "-"
		pathStr = "-"
		ip = "-"
	}

	errStr := fmt.Sprintf("%v", err)
	if len(errStr) > maxLen {
		errStr = errStr[:maxLen] + "…"
	}

	var msg strings.Builder
	msg.WriteString("<b>KIYA ALERT</b>\n\n")
	msg.WriteString("<b>Request</b>\n")
	msg.WriteString(fmt.Sprintf("%s %s\n", method, pathStr))
	if query != "" {
		msg.WriteString(fmt.Sprintf("Query: %s\n", util.HTMLEscape(query)))
	}
	msg.WriteString(fmt.Sprintf("IP: %s\n\n", ip))
	msg.WriteString("<b>Error</b>\n")
	msg.WriteString("<pre>")
	msg.WriteString(util.HTMLEscape(errStr))
	msg.WriteString("</pre>")

	sendTelegramHTML(msg.String())
}

func closeUnsafe() {
	if telegramCancel != nil {
		telegramCancel()
		telegramCancel = nil
	}

	if stopLogCh != nil {
		close(stopLogCh)
		logWg.Wait()
		stopLogCh = nil
	}

	logChan = nil

	telegramWg.Wait()

	logMutex.Lock()
	defer logMutex.Unlock()

	if logFile != nil {
		logFile.Close()
		logFile = nil
	}

	if wafLogFile != nil {
		wafLogFile.Close()
		wafLogFile = nil
	}
}

// Close closes all logger resources.
func Close() {
	logStateMu.Lock()
	defer logStateMu.Unlock()
	closeUnsafe()
}

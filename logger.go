package kiya

import (
	"net/http"

	"github.com/fuadarradhi/kiya/internal/logger"
)

// InitLogger menginisialisasi logger internal.
func InitLogger(debug bool, token, group string) {
	logger.Init(debug, token, group)
}

// CloseLogger menutup semua resource logger.
func CloseLogger() {
	logger.Close()
}

// LogInfo mencatat pesan level INFO.
func LogInfo(format string, v ...any) {
	logger.LogInfo(format, v...)
}

// LogWarn mencatat pesan level WARN.
func LogWarn(format string, v ...any) {
	logger.LogWarn(format, v...)
}

// LogError mencatat pesan level ERROR dan mengirim alert ke Telegram.
func LogError(format string, v ...any) {
	logger.LogError(format, v...)
}

// LogWAF mencatat serangan WAF/ATTACK dan mengirim alert ke Telegram.
func LogWAF(format string, v ...any) {
	logger.LogWAF(format, v...)
}

// LogTelegram mengirim pesan error custom beserta info request ke Telegram.
func LogTelegram(r *http.Request, err any) {
	logger.LogTelegram(r, err)
}

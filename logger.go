package kiya

import (
	"net/http"

	"github.com/fuadarradhi/kiya/internal/logger"
)

func InitLogger(debug bool, token, group string, enabled bool) {
	logger.Init(logger.Config{
		Debug:           debug,
		Token:           token,
		Group:           group,
		LogPath:         "./temp/log",
		WAFPath:         "./temp/waf",
		TelegramEnabled: enabled,
	})
}

func CloseLogger() {
	logger.Close()
}

func LogInfo(format string, v ...any) {
	logger.LogInfo(format, v...)
}

func LogWarn(format string, v ...any) {
	logger.LogWarn(format, v...)
}

func LogError(format string, v ...any) {
	logger.LogError(format, v...)
}

func LogWAF(format string, v ...any) {
	logger.LogWAF(format, v...)
}

func LogTelegram(r *http.Request, err any) {
	logger.LogTelegram(r, err)
}

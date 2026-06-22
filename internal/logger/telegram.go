package logger

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func sendTelegramDirect(text string) {
	logStateMu.RLock()
	token := telegramToken
	group := telegramGroup
	logStateMu.RUnlock()

	if token == "" || group == "" {
		return
	}

	key := getHash(text)
	telegramMutex.Lock()
	last, ok := telegramLastSent[key]
	if ok && time.Since(last) < 10*time.Minute {
		telegramMutex.Unlock()
		return
	}
	telegramLastSent[key] = time.Now()
	telegramMutex.Unlock()

	urlSend := "https://api.telegram.org/bot" + token + "/sendMessage"
	data := strings.NewReader(fmt.Sprintf("chat_id=%s&text=%s", group, url.QueryEscape(text)))

	doSendTelegram(urlSend, data)
}

func sendTelegramHTML(text string) {
	logStateMu.RLock()
	token := telegramToken
	group := telegramGroup
	logStateMu.RUnlock()

	if token == "" || group == "" {
		return
	}

	key := getHash(text)
	telegramMutex.Lock()
	last, ok := telegramLastSent[key]
	if ok && time.Since(last) < 5*time.Minute {
		telegramMutex.Unlock()
		return
	}
	telegramLastSent[key] = time.Now()
	telegramMutex.Unlock()

	urlSend := "https://api.telegram.org/bot" + token + "/sendMessage"
	data := strings.NewReader(fmt.Sprintf("chat_id=%s&text=%s&parse_mode=HTML&disable_web_page_preview=true",
		group, url.QueryEscape(text)))

	doSendTelegram(urlSend, data)
}

func doSendTelegram(urlSend string, data *strings.Reader) {
	req, err := http.NewRequest("POST", urlSend, data)
	if err != nil {
		log.Printf("Telegram request error: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("Telegram send error: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Telegram response error: %s", string(body))
	}
}

func getHash(s string) string {
	// Menggunakan crypto/sha1 yang sudah di-import di logger.go
	h := sha1.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))[:10]
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

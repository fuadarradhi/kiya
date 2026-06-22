package kiya

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reFormTag    = regexp.MustCompile(`(?i)<form\b[^>]*>`)
	reMethodAttr = regexp.MustCompile(`(?i)method\s*=\s*["']?\s*(\w+)`)
	reHeadTag    = regexp.MustCompile(`(?i)<head\b[^>]*>`)
)

func (r *Resources) Encrypt(plaintext []byte) (string, error) {
	if len(r.encryptKey) == 0 {
		return "", fmt.Errorf("encryption key not configured")
	}

	block, err := aes.NewCipher(r.encryptKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func (r *Resources) Decrypt(encoded string) ([]byte, error) {
	if len(r.encryptKey) == 0 {
		return nil, fmt.Errorf("encryption key not configured")
	}

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64url: %w", err)
	}

	block, err := aes.NewCipher(r.encryptKey)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce := data[:nonceSize]
	ciphertextWithTag := data[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertextWithTag, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}

func (r *Resources) EncryptString(plaintext string) (string, error) {
	return r.Encrypt([]byte(plaintext))
}

func (r *Resources) DecryptString(encoded string) (string, error) {
	plaintext, err := r.Decrypt(encoded)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (r *Resources) GenerateCSRFToken() (string, error) {
	if len(r.encryptKey) == 0 {
		return "", fmt.Errorf("encryption key not configured")
	}
	if r.Session == nil {
		return "", fmt.Errorf("session not available")
	}

	sessionID := r.Session.ID()
	if sessionID == "" {
		sessionID = fmt.Sprintf("%v", r.Session.Get("_t"))
	}

	timestamp := time.Now().Unix()
	plaintext := fmt.Sprintf("%s|%d", sessionID, timestamp)
	return r.EncryptString(plaintext)
}

func (r *Resources) VerifyCSRFToken(token string) bool {
	if len(r.encryptKey) == 0 || token == "" {
		return false
	}
	if r.Session == nil {
		return false
	}

	plaintext, err := r.DecryptString(token)
	if err != nil {
		return false
	}

	parts := strings.SplitN(plaintext, "|", 2)
	if len(parts) != 2 {
		return false
	}

	tokenSessionID := parts[0]
	timestampStr := parts[1]

	currentSessionID := r.Session.ID()
	if currentSessionID == "" {
		currentSessionID = fmt.Sprintf("%v", r.Session.Get("_t"))
	}

	if tokenSessionID != currentSessionID {
		return false
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return false
	}

	now := time.Now().Unix()
	elapsed := now - timestamp
	if elapsed < 0 || elapsed > 7200 {
		return false
	}

	return true
}

func (r *Resources) ExtractIP() string {
	if r.Request == nil {
		return ""
	}

	remoteIP, _, err := net.SplitHostPort(r.Request.RemoteAddr)
	if err != nil {
		remoteIP = r.Request.RemoteAddr
	}

	if isPrivateIP(remoteIP) {
		if xff := r.Request.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(parts[i])
				if ip != "" {
					return ip
				}
			}
		}

		if xri := r.Request.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	return remoteIP
}

func injectCSRFIntoForms(html string, token string) string {
	if token == "" {
		return html
	}

	if strings.Contains(html, `name="csrf_token"`) ||
		strings.Contains(html, `name='csrf_token'`) {
		return html
	}

	escapedToken := htmlEscape(token)
	csrfInput := fmt.Sprintf(
		`<input type="hidden" name="csrf_token" value="%s">`,
		escapedToken,
	)

	return reFormTag.ReplaceAllStringFunc(html, func(match string) string {
		methodMatches := reMethodAttr.FindStringSubmatch(match)

		method := "GET"
		if len(methodMatches) >= 2 && methodMatches[1] != "" {
			method = strings.ToUpper(methodMatches[1])
		}

		if method == "GET" {
			return match
		}

		return match + csrfInput
	})
}

func injectCSRFMeta(html string, token string) string {
	if token == "" {
		return html
	}

	if strings.Contains(html, `name="csrf-token"`) ||
		strings.Contains(html, `name='csrf-token'`) {
		return html
	}

	escapedToken := htmlEscape(token)
	meta := fmt.Sprintf(
		`<meta name="csrf-token" content="%s">`,
		escapedToken,
	)

	return reHeadTag.ReplaceAllStringFunc(html, func(match string) string {
		return match + meta
	})
}

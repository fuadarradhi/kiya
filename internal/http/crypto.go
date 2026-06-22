package http

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuadarradhi/kiya/internal/security"
	"github.com/fuadarradhi/kiya/internal/util"
)

var (
	reFormTag    = regexp.MustCompile(`(?i)<form\b[^>]*>`)
	reMethodAttr = regexp.MustCompile(`(?i)method\s*=\s*["']?\s*(\w+)`)
	reHeadTag    = regexp.MustCompile(`(?i)<head\b[^>]*>`)
)

// Encrypt encrypts plaintext using AES-GCM with the provided key.
func Encrypt(plaintext []byte, encryptKey []byte) (string, error) {
	if len(encryptKey) == 0 {
		return "", fmt.Errorf("encryption key not configured")
	}

	block, err := aes.NewCipher(encryptKey)
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

// Decrypt decrypts an AES-GCM encoded string.
func Decrypt(encoded string, encryptKey []byte) ([]byte, error) {
	if len(encryptKey) == 0 {
		return nil, fmt.Errorf("encryption key not configured")
	}

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64url: %w", err)
	}

	block, err := aes.NewCipher(encryptKey)
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

// EncryptString encrypts a string.
func EncryptString(plaintext string, encryptKey []byte) (string, error) {
	return Encrypt([]byte(plaintext), encryptKey)
}

// DecryptString decrypts a string.
func DecryptString(encoded string, encryptKey []byte) (string, error) {
	plaintext, err := Decrypt(encoded, encryptKey)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// GenerateCSRFToken creates a new CSRF token bound to the session.
func GenerateCSRFToken(session *security.Session, encryptKey []byte) (string, error) {
	if len(encryptKey) == 0 {
		return "", fmt.Errorf("encryption key not configured")
	}
	if session == nil {
		return "", fmt.Errorf("session not available")
	}

	sessionID := session.ID()
	if sessionID == "" {
		sessionID = fmt.Sprintf("%v", session.Get("_t"))
	}

	timestamp := time.Now().Unix()
	plaintext := fmt.Sprintf("%s|%d", sessionID, timestamp)
	return EncryptString(plaintext, encryptKey)
}

// VerifyCSRFToken validates a CSRF token against the current session.
func VerifyCSRFToken(token string, session *security.Session, encryptKey []byte) bool {
	if len(encryptKey) == 0 || token == "" {
		return false
	}
	if session == nil {
		return false
	}

	plaintext, err := DecryptString(token, encryptKey)
	if err != nil {
		return false
	}

	parts := strings.SplitN(plaintext, "|", 2)
	if len(parts) != 2 {
		return false
	}

	tokenSessionID := parts[0]
	timestampStr := parts[1]

	currentSessionID := session.ID()
	if currentSessionID == "" {
		currentSessionID = fmt.Sprintf("%v", session.Get("_t"))
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

// ExtractIP extracts the real IP address from the request.
func ExtractIP(req *http.Request) string {
	if req == nil {
		return ""
	}

	remoteIP, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		remoteIP = req.RemoteAddr
	}

	if util.IsPrivateIP(remoteIP) {
		if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(parts[i])
				if ip != "" {
					return ip
				}
			}
		}

		if xri := req.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	return remoteIP
}

// InjectCSRFIntoForms injects a hidden input into HTML forms.
func InjectCSRFIntoForms(html string, token string) string {
	if token == "" {
		return html
	}

	if strings.Contains(html, `name="csrf_token"`) ||
		strings.Contains(html, `name='csrf_token'`) {
		return html
	}

	escapedToken := util.HTMLEscape(token)
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

// InjectCSRFMeta injects a meta tag into the HTML head.
func InjectCSRFMeta(html string, token string) string {
	if token == "" {
		return html
	}

	if strings.Contains(html, `name="csrf-token"`) ||
		strings.Contains(html, `name='csrf-token'`) {
		return html
	}

	escapedToken := util.HTMLEscape(token)
	meta := fmt.Sprintf(
		`<meta name="csrf-token" content="%s">`,
		escapedToken,
	)

	return reHeadTag.ReplaceAllStringFunc(html, func(match string) string {
		return match + meta
	})
}

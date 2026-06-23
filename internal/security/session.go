package security

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/sessions"
)

type Session struct {
	raw   *sessions.Session
	req   *http.Request
	w     http.ResponseWriter
	dirty bool
	mu    sync.RWMutex
}

func NewSession(raw *sessions.Session, r *http.Request, w http.ResponseWriter) *Session {
	return &Session{
		raw:   raw,
		req:   r,
		w:     w,
		dirty: false,
	}
}

func (s *Session) Get(key string) any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.raw == nil {
		return nil
	}

	val, exists := s.raw.Values[key]
	if !exists {
		return nil
	}
	return val
}

func (s *Session) Set(key string, val any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.raw.Values[key] = val
	s.dirty = true
}

func (s *Session) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.raw.Values, key)
	s.dirty = true
}

func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k := range s.raw.Values {
		delete(s.raw.Values, k)
	}
	s.dirty = true
}

func (s *Session) Destroy() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.raw.Values = make(map[any]any)
	s.raw.Options.MaxAge = -1
	s.dirty = true
}

func (s *Session) RegenerateID() {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random bytes for session ID: " + err.Error())
	}

	s.raw.ID = hex.EncodeToString(b)
	s.dirty = true
}

func (s *Session) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty {
		return nil
	}

	err := s.raw.Save(s.req, s.w)
	if err != nil {
		s.dirty = true
		return err
	}

	s.dirty = false
	return nil
}

func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raw == nil {
		return ""
	}
	return s.raw.ID
}

func (s *Session) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]string, 0, len(s.raw.Values))
	for k := range s.raw.Values {
		if strKey, ok := k.(string); ok {
			keys = append(keys, strKey)
		}
	}
	return keys
}

func (s *Session) Flash(key string, val any) {
	s.Set("_flash_"+key, val)
}

func (s *Session) GetFlash(key string) any {
	fullKey := "_flash_" + key

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.raw == nil {
		return nil
	}

	val, exists := s.raw.Values[fullKey]
	if !exists {
		return nil
	}

	delete(s.raw.Values, fullKey)
	s.dirty = true

	return val
}

func (s *Session) SetString(key, val string) {
	s.Set(key, val)
}

func (s *Session) GetString(key string) string {
	v := s.Get(key)
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func (s *Session) SetInt(key string, val int) {
	s.Set(key, val)
}

func (s *Session) GetInt(key string) int {
	return int(s.GetInt64(key))
}

func (s *Session) SetInt64(key string, val int64) {
	s.Set(key, val)
}

func (s *Session) GetInt64(key string) int64 {
	v := s.Get(key)
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case int:
		return int64(t)
	case int8:
		return int64(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case float32:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		i, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0
		}
		return i
	default:
		return 0
	}
}

func (s *Session) SetFloat(key string, val float64) {
	s.Set(key, val)
}

func (s *Session) GetFloat(key string) float64 {
	v := s.Get(key)
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case float32:
		return float64(t)
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

func (s *Session) SetBool(key string, val bool) {
	s.Set(key, val)
}

func (s *Session) GetBool(key string) bool {
	v := s.Get(key)
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, err := strconv.ParseBool(t)
		if err != nil {
			return false
		}
		return b
	case int:
		return t != 0
	default:
		return false
	}
}

func (s *Session) SetBytes(key string, val []byte) {
	s.Set(key, val)
}

func (s *Session) GetBytes(key string) []byte {
	v := s.Get(key)
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case []byte:
		return t
	case string:
		return []byte(t)
	default:
		return nil
	}
}

func (s *Session) SetTime(key string, val time.Time) {
	s.Set(key, val)
}

func (s *Session) GetTime(key string) time.Time {
	v := s.Get(key)
	if v == nil {
		return time.Time{}
	}
	if t, ok := v.(time.Time); ok {
		return t
	}
	return time.Time{}
}

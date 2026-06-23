package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
	"github.com/redis/go-redis/v9"
)

// RedisStore stores sessions in a Redis backend.
type RedisStore struct {
	client       *redis.Client
	codecs       []securecookie.Codec
	options      *sessions.Options
	maxAge       int
	prefix       string
	serializer   SessionSerializer
	keyGenerator func(*http.Request) string
	mu           sync.Mutex
}

// NewRedisStore creates a new RedisStore instance.
func NewRedisStore(addr, password string, db int, keyPairs []byte, maxAge int, sameSite http.SameSite) (*RedisStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	if maxAge <= 0 {
		maxAge = 86400 * 7
	}

	store := &RedisStore{
		client: client,
		codecs: securecookie.CodecsFromPairs(keyPairs),
		options: &sessions.Options{
			Path:     "/",
			MaxAge:   maxAge,
			HttpOnly: true,
			Secure:   true,
			SameSite: sameSite,
		},
		maxAge:     maxAge,
		prefix:     "session_",
		serializer: &JSONSerializer{},
	}

	return store, nil
}

// Get returns a session for the given name after adding it to the registry.
func (s *RedisStore) Get(r *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(r).Get(s, name)
}

// New creates a new session for the given name.
func (s *RedisStore) New(r *http.Request, name string) (*sessions.Session, error) {
	session := sessions.NewSession(s, name)
	opts := *s.options
	session.Options = &opts
	session.IsNew = true

	c, err := r.Cookie(name)
	if err != nil {
		return session, nil
	}

	session.ID = c.Value
	err = s.load(session)
	if err != nil {
		// If session doesn't exist in Redis, treat as new
		session.ID = ""
		session.IsNew = true
		return session, nil
	}

	session.IsNew = false
	return session, nil
}

// Save adds a single session to the response.
func (s *RedisStore) Save(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	if session.Options.MaxAge <= 0 {
		if err := s.delete(session); err != nil {
			return err
		}
		http.SetCookie(w, sessions.NewCookie(session.Name(), "", session.Options))
		return nil
	}

	if session.ID == "" {
		id := securecookie.GenerateRandomKey(32)
		session.ID = base64.URLEncoding.EncodeToString(id)
	}

	if err := s.save(session); err != nil {
		return err
	}

	http.SetCookie(w, sessions.NewCookie(session.Name(), session.ID, session.Options))
	return nil
}

func (s *RedisStore) save(session *sessions.Session) error {
	b, err := s.serializer.Serialize(session)
	if err != nil {
		return err
	}

	expiration := time.Duration(s.options.MaxAge) * time.Second
	key := s.prefix + session.ID

	ctx := context.Background()
	err = s.client.Set(ctx, key, b, expiration).Err()
	if err != nil {
		return fmt.Errorf("redis set error: %w", err)
	}

	return nil
}

func (s *RedisStore) load(session *sessions.Session) error {
	key := s.prefix + session.ID
	ctx := context.Background()

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return fmt.Errorf("session not found")
		}
		return err
	}

	return s.serializer.Deserialize(data, session)
}

func (s *RedisStore) delete(session *sessions.Session) error {
	if session.ID == "" {
		return nil
	}
	key := s.prefix + session.ID
	ctx := context.Background()
	return s.client.Del(ctx, key).Err()
}

// SessionSerializer represents a serializer for session data.
type SessionSerializer interface {
	Serialize(s *sessions.Session) ([]byte, error)
	Deserialize(d []byte, s *sessions.Session) error
}

// JSONSerializer encodes/decodes session data to JSON.
type JSONSerializer struct{}

// Serialize encodes the session values to JSON.
func (s JSONSerializer) Serialize(sess *sessions.Session) ([]byte, error) {
	m := make(map[string]interface{}, len(sess.Values))
	for k, v := range sess.Values {
		ks, ok := k.(string)
		if !ok {
			return nil, fmt.Errorf("non-string session key: %v", k)
		}
		m[ks] = v
	}
	return json.Marshal(m)
}

// Deserialize decodes the JSON data back to session values.
func (s JSONSerializer) Deserialize(d []byte, sess *sessions.Session) error {
	m := make(map[string]interface{})
	err := json.Unmarshal(d, &m)
	if err != nil {
		return err
	}
	for k, v := range m {
		sess.Values[k] = v
	}
	return nil
}

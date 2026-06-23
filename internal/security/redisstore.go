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

func NewRedisStore(addr, password string, db int, keyPairs []byte, maxAge int, sameSite http.SameSite, secureCookie bool) (*RedisStore, error) {
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
			Secure:   secureCookie,
			SameSite: sameSite,
		},
		maxAge:     maxAge,
		prefix:     "session_",
		serializer: &JSONSerializer{},
	}

	return store, nil
}

func (s *RedisStore) Get(r *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(r).Get(s, name)
}

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
	err = s.load(r.Context(), session)
	if err != nil {
		session.ID = ""
		session.IsNew = true
		return session, nil
	}

	session.IsNew = false
	return session, nil
}

func (s *RedisStore) Save(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	if session.Options.MaxAge <= 0 {
		if err := s.delete(r.Context(), session); err != nil {
			return err
		}
		http.SetCookie(w, sessions.NewCookie(session.Name(), "", session.Options))
		return nil
	}

	if session.ID == "" {
		id := securecookie.GenerateRandomKey(32)
		session.ID = base64.URLEncoding.EncodeToString(id)
	}

	if err := s.save(r.Context(), session); err != nil {
		return err
	}

	http.SetCookie(w, sessions.NewCookie(session.Name(), session.ID, session.Options))
	return nil
}

func (s *RedisStore) save(ctx context.Context, session *sessions.Session) error {
	b, err := s.serializer.Serialize(session)
	if err != nil {
		return err
	}

	expiration := time.Duration(s.options.MaxAge) * time.Second
	key := s.prefix + session.ID

	err = s.client.Set(ctx, key, b, expiration).Err()
	if err != nil {
		return fmt.Errorf("redis set error: %w", err)
	}

	return nil
}

func (s *RedisStore) load(ctx context.Context, session *sessions.Session) error {
	key := s.prefix + session.ID

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return fmt.Errorf("session not found")
		}
		return err
	}

	return s.serializer.Deserialize(data, session)
}

func (s *RedisStore) delete(ctx context.Context, session *sessions.Session) error {
	if session.ID == "" {
		return nil
	}
	key := s.prefix + session.ID
	return s.client.Del(ctx, key).Err()
}

type SessionSerializer interface {
	Serialize(s *sessions.Session) ([]byte, error)
	Deserialize(d []byte, s *sessions.Session) error
}

type JSONSerializer struct{}

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

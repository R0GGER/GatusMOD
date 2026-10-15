// Package session keeps login sessions and failed logins in memory, for the admin and the private status pages.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	TTL           = 12 * time.Hour
	MaxFailures   = 5
	LockoutPeriod = 15 * time.Minute
)

// Session is a login. Hash is the password hash it was created with: a session whose hash no longer matches the
// current password is invalid, so changing a password logs everybody out.
type Session struct {
	User    string
	Hash    string
	CSRF    string
	Expires time.Time
}

type failure struct {
	count int
	since time.Time
}

// Store is kept in memory: a restart of GatusMOD logs everybody out.
type Store struct {
	mu       sync.Mutex
	byID     map[string]Session
	failures map[string]failure
}

func NewStore() *Store {
	return &Store{byID: map[string]Session{}, failures: map[string]failure{}}
}

func RandomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) Create(user, hash string) (string, Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, sess := range s.byID {
		if now.After(sess.Expires) {
			delete(s.byID, id)
		}
	}
	id := RandomToken()
	sess := Session{User: user, Hash: hash, CSRF: RandomToken(), Expires: now.Add(TTL)}
	s.byID[id] = sess
	return id, sess
}

func (s *Store) Get(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if ok && time.Now().After(sess.Expires) {
		delete(s.byID, id)
		return Session{}, false
	}
	return sess, ok
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.byID, id)
	s.mu.Unlock()
}

// ReplaceAll ends every session except keep, which continues with the new password hash.
func (s *Store) ReplaceAll(keep, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.byID {
		if id != keep {
			delete(s.byID, id)
			continue
		}
		sess.Hash = hash
		s.byID[id] = sess
	}
}

// Locked reports how long the client still has to wait after too many failed logins.
func (s *Store) Locked(ip string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.failures[ip]
	if !ok {
		return 0
	}
	if wait := time.Until(f.since.Add(LockoutPeriod)); wait > 0 {
		if f.count >= MaxFailures {
			return wait
		}
		return 0
	}
	delete(s.failures, ip)
	return 0
}

func (s *Store) Fail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failures[ip]
	if f.count == 0 || time.Since(f.since) > LockoutPeriod {
		f = failure{since: time.Now()}
	}
	f.count++
	s.failures[ip] = f
}

func (s *Store) Succeed(ip string) {
	s.mu.Lock()
	delete(s.failures, ip)
	s.mu.Unlock()
}

func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func IsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// SameOrigin reports whether a form post comes from the site itself.
func SameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

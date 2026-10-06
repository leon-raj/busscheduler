// Package results caches plan results by result_id.
package results

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/example/busscheduler/internal/model"
)

type Status string

const (
	Pending   Status = "pending"
	Completed Status = "completed"
	Failed    Status = "failed"
)

type Entry struct {
	ID        string
	Status    Status
	Result    *model.Result
	Error     string
	CreatedAt time.Time
}

// Store is a TTL + size-bounded in-memory cache. Results vanish on restart;
// persist them elsewhere if you need durability.
type Store struct {
	TTL time.Duration // default 24h
	Max int           // default 1000

	mu      sync.Mutex
	entries map[string]*Entry
	order   []string // insertion order, oldest first
}

func NewID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return "res_" + hex.EncodeToString(b)
}

func (s *Store) Reserve(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gc()
	if s.entries == nil {
		s.entries = map[string]*Entry{}
	}
	s.entries[id] = &Entry{ID: id, Status: Pending, CreatedAt: time.Now()}
	s.order = append(s.order, id)
}

func (s *Store) Complete(id string, r *model.Result) {
	s.set(id, func(e *Entry) { e.Status, e.Result = Completed, r })
}

func (s *Store) Fail(id string, err error) {
	s.set(id, func(e *Entry) { e.Status, e.Error = Failed, err.Error() })
}

func (s *Store) set(id string, f func(*Entry)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok {
		f(e)
	}
}

// Get returns a copy of the entry, or false if unknown or expired.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gc()
	e, ok := s.entries[id]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// gc drops expired entries and, if over Max, the oldest ones. Caller holds mu.
func (s *Store) gc() {
	ttl, max := s.TTL, s.Max
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if max <= 0 {
		max = 1000
	}
	now := time.Now()
	keep := s.order[:0]
	for _, id := range s.order {
		e, ok := s.entries[id]
		if !ok {
			continue
		}
		if now.Sub(e.CreatedAt) > ttl {
			delete(s.entries, id)
			continue
		}
		keep = append(keep, id)
	}
	s.order = keep
	for len(s.order) > max {
		delete(s.entries, s.order[0])
		s.order = s.order[1:]
	}
}

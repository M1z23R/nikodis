package cache

import (
	"sync"
	"time"

	"github.com/dimitrije/nikodis/internal/debuglog"
)

type entry struct {
	value  []byte
	expiry time.Time // zero means no expiry
}

func (e entry) expired() bool {
	return !e.expiry.IsZero() && time.Now().After(e.expiry)
}

type Store struct {
	mu     sync.RWMutex
	items  map[string]entry
	logger debuglog.Logger
	done   chan struct{}
}

// New creates a cache store. Pass nil logger for no debug logging.
func New(logger debuglog.Logger) *Store {
	if logger == nil {
		logger = debuglog.NewNopLogger()
	}
	s := &Store{
		items:  make(map[string]entry),
		logger: logger,
		done:   make(chan struct{}),
	}
	go s.reaper()
	return s
}

func (s *Store) Set(key string, value []byte, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := entry{value: value}
	if ttl > 0 {
		e.expiry = time.Now().Add(ttl)
	}
	s.items[key] = e
	s.logger.Log(debuglog.Event{
		Component: "cache",
		Action:    "cache_set",
		Key:       key,
	})
}

func (s *Store) Get(key string) ([]byte, bool) {
	s.mu.RLock()
	e, ok := s.items[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if e.expired() {
		s.Delete(key)
		return nil, false
	}
	s.logger.Log(debuglog.Event{
		Component: "cache",
		Action:    "cache_get",
		Key:       key,
	})
	return e.value, true
}

func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.items[key]
	if ok {
		delete(s.items, key)
		s.logger.Log(debuglog.Event{
			Component: "cache",
			Action:    "cache_delete",
			Key:       key,
		})
	}
	return ok
}

func (s *Store) Close() {
	close(s.done)
}

func (s *Store) reaper() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			for k, e := range s.items {
				if e.expired() {
					delete(s.items, k)
					s.logger.Log(debuglog.Event{
						Component: "cache",
						Action:    "cache_expire",
						Key:       k,
					})
				}
			}
			s.mu.Unlock()
		}
	}
}

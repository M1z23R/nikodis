package broker

import (
	"sync/atomic"

	"github.com/google/uuid"
)

type Subscriber struct {
	id     string
	C      chan *Message
	paused atomic.Bool
}

func newSubscriber() *Subscriber {
	return &Subscriber{
		id: uuid.New().String(),
		C:  make(chan *Message, 64),
	}
}

func (s *Subscriber) pause()         { s.paused.Store(true) }
func (s *Subscriber) resume()        { s.paused.Store(false) }
func (s *Subscriber) isPaused() bool { return s.paused.Load() }

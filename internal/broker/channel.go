package broker

import (
	"sync"
	"time"
)

type inflight struct {
	msg        *Message
	subscriber *Subscriber
	timer      *time.Timer
}

type channel struct {
	mu          sync.Mutex
	pending     *queue
	inflight    map[string]*inflight
	subscribers []*Subscriber
	rrIndex     int
	onDrop      func(msg *Message)    // called when message exceeds max redeliveries
	onOverflow  func(msg *Message)    // called when buffer overflow drops oldest message
	onDeliver   func(msg *Message)    // called when message is delivered to subscriber
	onTimeout   func(msg *Message)    // called when ack timeout fires
	onRequeue   func(msg *Message)    // called when message is requeued
}

func newChannel(maxBufferSize int) *channel {
	return &channel{
		pending:  newQueue(maxBufferSize),
		inflight: make(map[string]*inflight),
	}
}

func (c *channel) addSubscriber(sub *Subscriber) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscribers = append(c.subscribers, sub)
	c.drainPending()
}

func (c *channel) removeSubscriber(sub *Subscriber) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range c.subscribers {
		if s.id == sub.id {
			c.subscribers = append(c.subscribers[:i], c.subscribers[i+1:]...)
			break
		}
	}
	// Requeue inflight messages for this subscriber
	for id, inf := range c.inflight {
		if inf.subscriber.id == sub.id {
			inf.timer.Stop()
			delete(c.inflight, id)
			c.pending.push(inf.msg)
			if c.onRequeue != nil {
				c.onRequeue(inf.msg)
			}
		}
	}
	c.drainPending()
}

func (c *channel) enqueue(msg *Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dropped := c.pending.push(msg); dropped != nil && c.onOverflow != nil {
		c.onOverflow(dropped)
	}
	c.drainPending()
}

func (c *channel) resumeSubscriber() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drainPending()
}

// drainPending delivers pending messages to eligible subscribers. Must hold c.mu.
func (c *channel) drainPending() {
	for c.pending.len() > 0 {
		sub := c.nextEligibleSubscriber()
		if sub == nil {
			return
		}
		msg := c.pending.pop()
		if !c.deliver(msg, sub) {
			return // subscriber buffer full, stop draining
		}
	}
}

func (c *channel) nextEligibleSubscriber() *Subscriber {
	n := len(c.subscribers)
	if n == 0 {
		return nil
	}
	for i := 0; i < n; i++ {
		idx := (c.rrIndex + i) % n
		sub := c.subscribers[idx]
		if !sub.isPaused() {
			c.rrIndex = (idx + 1) % n
			return sub
		}
	}
	return nil
}

// deliver sends a message to a subscriber. Returns true if sent, false if subscriber buffer full.
func (c *channel) deliver(msg *Message, sub *Subscriber) bool {
	msg.DeliveryAttempt++
	inf := &inflight{msg: msg, subscriber: sub}
	inf.timer = time.AfterFunc(msg.AckTimeout, func() {
		c.handleTimeout(msg.ID)
	})
	c.inflight[msg.ID] = inf
	// Send a copy so the consumer can read fields without racing with redelivery
	msgCopy := *msg
	select {
	case sub.C <- &msgCopy:
		if c.onDeliver != nil {
			c.onDeliver(msg)
		}
		return true
	default:
		// Subscriber buffer full — requeue
		inf.timer.Stop()
		delete(c.inflight, msg.ID)
		c.pending.push(msg)
		return false
	}
}

func (c *channel) ack(messageID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	inf, ok := c.inflight[messageID]
	if !ok {
		return false
	}
	inf.timer.Stop()
	delete(c.inflight, messageID)
	return true
}

func (c *channel) handleTimeout(messageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	inf, ok := c.inflight[messageID]
	if !ok {
		return
	}
	delete(c.inflight, messageID)
	if c.onTimeout != nil {
		c.onTimeout(inf.msg)
	}
	if inf.msg.DeliveryAttempt > inf.msg.MaxRedeliveries {
		if c.onDrop != nil {
			c.onDrop(inf.msg)
		}
		return // drop
	}
	c.pending.push(inf.msg)
	if c.onRequeue != nil {
		c.onRequeue(inf.msg)
	}
	c.drainPending()
}

func (c *channel) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, inf := range c.inflight {
		inf.timer.Stop()
	}
}

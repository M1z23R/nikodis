package broker

import (
	"sync"
	"time"

	"github.com/M1z23R/nikodis/internal/debuglog"
)

type Config struct {
	MaxBufferSize     int
	DefaultAckTimeout time.Duration
	MaxRedeliveries   uint32
}

type Broker struct {
	cfg      Config
	mu       sync.Mutex
	channels map[string]*channel
	logger   debuglog.Logger
}

func New(cfg Config, logger debuglog.Logger) *Broker {
	if logger == nil {
		logger = debuglog.NewNopLogger()
	}
	return &Broker{
		cfg:      cfg,
		channels: make(map[string]*channel),
		logger:   logger,
	}
}

func (b *Broker) getOrCreateChannel(name string) *channel {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.channels[name]
	if !ok {
		ch = newChannel(b.cfg.MaxBufferSize)
		ch.onDrop = func(msg *Message) {
			b.logger.Log(debuglog.Event{
				Component: "broker",
				Action:    "drop_max_redelivery",
				Channel:   name,
				MessageID: msg.ID,
			})
		}
		ch.onOverflow = func(msg *Message) {
			b.logger.Log(debuglog.Event{
				Component: "broker",
				Action:    "drop_buffer_overflow",
				Channel:   name,
				MessageID: msg.ID,
			})
		}
		ch.onDeliver = func(msg *Message) {
			b.logger.Log(debuglog.Event{
				Component: "broker",
				Action:    "deliver",
				Channel:   name,
				MessageID: msg.ID,
			})
		}
		ch.onTimeout = func(msg *Message) {
			b.logger.Log(debuglog.Event{
				Component: "broker",
				Action:    "ack_timeout",
				Channel:   name,
				MessageID: msg.ID,
			})
		}
		ch.onRequeue = func(msg *Message) {
			b.logger.Log(debuglog.Event{
				Component: "broker",
				Action:    "requeue",
				Channel:   name,
				MessageID: msg.ID,
			})
		}
		b.channels[name] = ch
	}
	return ch
}

func (b *Broker) Publish(channelName string, data []byte, ackTimeout time.Duration) (string, error) {
	if ackTimeout <= 0 {
		ackTimeout = b.cfg.DefaultAckTimeout
	}
	msg := newMessage(channelName, data, ackTimeout, b.cfg.MaxRedeliveries)
	ch := b.getOrCreateChannel(channelName)
	ch.enqueue(msg)
	b.logger.Log(debuglog.Event{
		Component: "broker",
		Action:    "publish",
		Channel:   channelName,
		MessageID: msg.ID,
	})
	return msg.ID, nil
}

func (b *Broker) Subscribe(channelName string) (*Subscriber, error) {
	ch := b.getOrCreateChannel(channelName)
	sub := newSubscriber()
	ch.addSubscriber(sub)
	return sub, nil
}

func (b *Broker) Unsubscribe(sub *Subscriber, channelName string) {
	b.mu.Lock()
	ch, ok := b.channels[channelName]
	b.mu.Unlock()
	if !ok {
		return
	}
	ch.removeSubscriber(sub)
}

func (b *Broker) Ack(messageID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.channels {
		if ch.ack(messageID) {
			b.logger.Log(debuglog.Event{
				Component: "broker",
				Action:    "ack",
				MessageID: messageID,
			})
			return true
		}
	}
	return false
}

func (b *Broker) Pause(sub *Subscriber) {
	sub.pause()
}

func (b *Broker) Resume(sub *Subscriber, channelName string) {
	sub.resume()
	b.mu.Lock()
	ch, ok := b.channels[channelName]
	b.mu.Unlock()
	if ok {
		ch.resumeSubscriber()
	}
}

func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.channels {
		ch.close()
	}
}

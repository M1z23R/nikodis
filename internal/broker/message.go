package broker

import (
	"time"

	"github.com/google/uuid"
)

type Message struct {
	ID              string
	Channel         string
	Data            []byte
	AckTimeout      time.Duration
	DeliveryAttempt uint32
	MaxRedeliveries uint32
}

func newMessage(channel string, data []byte, ackTimeout time.Duration, maxRedeliveries uint32) *Message {
	return &Message{
		ID:              uuid.New().String(),
		Channel:         channel,
		Data:            data,
		AckTimeout:      ackTimeout,
		DeliveryAttempt: 0,
		MaxRedeliveries: maxRedeliveries,
	}
}

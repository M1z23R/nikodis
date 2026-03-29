package client

import (
	"sync"

	pb "github.com/dimitrije/nikodis/pkg/gen"
)

type Message struct {
	ID              string
	Channel         string
	Data            []byte
	DeliveryAttempt uint32
	stream          pb.BrokerService_SubscribeClient
}

func (m *Message) Ack() error {
	return m.stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Ack{Ack: &pb.Ack{MessageId: m.ID}},
	})
}

// Subscription represents a subscription to one or more channels.
// In exclusive mode, one Subscription receives from all channels.
// In independent mode, each channel gets its own Subscription.
type Subscription struct {
	stream      pb.BrokerService_SubscribeClient
	msgCh       chan *Message
	done        chan struct{}
	closeOnce   sync.Once
	sharedClose *sync.Once  // non-nil for independent mode (shared across subscriptions)
	channel     string      // set for independent mode subscriptions, empty for exclusive
}

// --- Exclusive mode ---

func newExclusiveSubscription(stream pb.BrokerService_SubscribeClient) *Subscription {
	s := &Subscription{
		stream: stream,
		msgCh:  make(chan *Message, 64),
		done:   make(chan struct{}),
	}
	go s.recv()
	return s
}

func (s *Subscription) recv() {
	defer close(s.msgCh)
	for {
		pbMsg, err := s.stream.Recv()
		if err != nil {
			return
		}
		msg := &Message{
			ID:              pbMsg.Id,
			Channel:         pbMsg.Channel,
			Data:            pbMsg.Data,
			DeliveryAttempt: pbMsg.DeliveryAttempt,
			stream:          s.stream,
		}
		select {
		case s.msgCh <- msg:
		case <-s.done:
			return
		}
	}
}

func (s *Subscription) Messages() <-chan *Message {
	return s.msgCh
}

func (s *Subscription) Pause() error {
	return s.stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Pause{Pause: &pb.Pause{Channel: s.channel}},
	})
}

func (s *Subscription) Resume() error {
	return s.stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Resume{Resume: &pb.Resume{Channel: s.channel}},
	})
}

func (s *Subscription) Add(channel string) error {
	return s.stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Subscribe{
			Subscribe: &pb.SubscribeRequest{Channel: channel},
		},
	})
}

func (s *Subscription) Remove(channel string) error {
	return s.stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Unsubscribe{
			Unsubscribe: &pb.UnsubscribeRequest{Channel: channel},
		},
	})
}

func (s *Subscription) Close() error {
	closer := &s.closeOnce
	if s.sharedClose != nil {
		closer = s.sharedClose
	}
	closer.Do(func() {
		close(s.done)
		s.stream.CloseSend()
	})
	return nil
}

// --- Independent mode ---

func newIndependentSubscriptions(stream pb.BrokerService_SubscribeClient, channels []string) map[string]*Subscription {
	subs := make(map[string]*Subscription, len(channels))
	done := make(chan struct{})
	sharedClose := &sync.Once{}
	for _, ch := range channels {
		subs[ch] = &Subscription{
			stream:      stream,
			msgCh:       make(chan *Message, 64),
			done:        done,
			channel:     ch,
			sharedClose: sharedClose,
		}
	}

	// Single recv goroutine demuxes messages to per-channel subscriptions
	go func() {
		defer func() {
			for _, sub := range subs {
				close(sub.msgCh)
			}
		}()
		for {
			pbMsg, err := stream.Recv()
			if err != nil {
				return
			}
			sub, ok := subs[pbMsg.Channel]
			if !ok {
				continue // message for unknown channel, skip
			}
			msg := &Message{
				ID:              pbMsg.Id,
				Channel:         pbMsg.Channel,
				Data:            pbMsg.Data,
				DeliveryAttempt: pbMsg.DeliveryAttempt,
				stream:          stream,
			}
			select {
			case sub.msgCh <- msg:
			case <-done:
				return
			}
		}
	}()

	return subs
}

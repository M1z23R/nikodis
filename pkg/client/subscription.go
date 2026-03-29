package client

import (
	"sync"

	pb "github.com/dimitrije/nikodis/pkg/gen"
)

// sendMu protects concurrent stream.Send calls. Shared across all
// subscriptions on the same gRPC stream (relevant for independent mode
// where multiple goroutines may ack concurrently).
type sendMu struct {
	mu     sync.Mutex
	stream pb.BrokerService_SubscribeClient
}

func (s *sendMu) send(msg *pb.SubscribeStream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(msg)
}

type Message struct {
	ID              string
	Channel         string
	Data            []byte
	DeliveryAttempt uint32
	sender          *sendMu
}

func (m *Message) Ack() error {
	return m.sender.send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Ack{Ack: &pb.Ack{MessageId: m.ID}},
	})
}

// Subscription represents a subscription to one or more channels.
// In exclusive mode, one Subscription receives from all channels.
// In independent mode, each channel gets its own Subscription.
type Subscription struct {
	sender      *sendMu
	msgCh       chan *Message
	done        chan struct{}
	closeOnce   sync.Once
	sharedClose *sync.Once // non-nil for independent mode (shared across subscriptions)
	channel     string     // set for independent mode subscriptions, empty for exclusive
}

// --- Exclusive mode ---

func newExclusiveSubscription(stream pb.BrokerService_SubscribeClient) *Subscription {
	s := &Subscription{
		sender: &sendMu{stream: stream},
		msgCh:  make(chan *Message, 64),
		done:   make(chan struct{}),
	}
	go s.recv(stream)
	return s
}

func (s *Subscription) recv(stream pb.BrokerService_SubscribeClient) {
	defer close(s.msgCh)
	for {
		pbMsg, err := stream.Recv()
		if err != nil {
			return
		}
		msg := &Message{
			ID:              pbMsg.Id,
			Channel:         pbMsg.Channel,
			Data:            pbMsg.Data,
			DeliveryAttempt: pbMsg.DeliveryAttempt,
			sender:          s.sender,
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
	return s.sender.send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Pause{Pause: &pb.Pause{Channel: s.channel}},
	})
}

func (s *Subscription) Resume() error {
	return s.sender.send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Resume{Resume: &pb.Resume{Channel: s.channel}},
	})
}

func (s *Subscription) Add(channel string) error {
	return s.sender.send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Subscribe{
			Subscribe: &pb.SubscribeRequest{Channel: channel},
		},
	})
}

func (s *Subscription) Remove(channel string) error {
	return s.sender.send(&pb.SubscribeStream{
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
		s.sender.mu.Lock()
		defer s.sender.mu.Unlock()
		s.sender.stream.CloseSend()
	})
	return nil
}

// --- Independent mode ---

func newIndependentSubscriptions(stream pb.BrokerService_SubscribeClient, channels []string) map[string]*Subscription {
	sender := &sendMu{stream: stream}
	subs := make(map[string]*Subscription, len(channels))
	done := make(chan struct{})
	sharedClose := &sync.Once{}
	for _, ch := range channels {
		subs[ch] = &Subscription{
			sender:      sender,
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
				sender:          sender,
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

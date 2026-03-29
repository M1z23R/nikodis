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

type Subscription struct {
	stream    pb.BrokerService_SubscribeClient
	msgCh     chan *Message
	done      chan struct{}
	closeOnce sync.Once
}

func newSubscription(stream pb.BrokerService_SubscribeClient) *Subscription {
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
		Action: &pb.SubscribeStream_Pause{Pause: &pb.Pause{}},
	})
}

func (s *Subscription) Resume() error {
	return s.stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Resume{Resume: &pb.Resume{}},
	})
}

func (s *Subscription) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.stream.CloseSend()
	})
	return nil
}

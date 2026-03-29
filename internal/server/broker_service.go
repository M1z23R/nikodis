package server

import (
	"context"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/dimitrije/nikodis/pkg/gen"

	"github.com/dimitrije/nikodis/internal/broker"
)

type BrokerService struct {
	pb.UnimplementedBrokerServiceServer
	broker *broker.Broker
}

func NewBrokerService(b *broker.Broker) *BrokerService {
	return &BrokerService{broker: b}
}

func (s *BrokerService) Publish(ctx context.Context, req *pb.PublishRequest) (*pb.PublishResponse, error) {
	ns := extractNamespace(ctx)
	ch := namespacedKey(ns, req.Channel)
	var ackTimeout time.Duration
	if req.AckTimeoutSeconds != nil {
		ackTimeout = time.Duration(*req.AckTimeoutSeconds) * time.Second
	}
	msgID, err := s.broker.Publish(ch, req.Data, ackTimeout)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "publish: %v", err)
	}
	return &pb.PublishResponse{MessageId: msgID}, nil
}

type channelSub struct {
	sub     *broker.Subscriber
	channel string // namespaced channel name
	rawName string // original channel name from client
}

func (s *BrokerService) Subscribe(stream grpc.BidiStreamingServer[pb.SubscribeStream, pb.Message]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	initReq := first.GetInit()
	if initReq == nil {
		return status.Error(codes.InvalidArgument, "first message must be SubscribeInit")
	}

	ns := extractNamespace(stream.Context())
	exclusive := initReq.DeliveryMode == pb.DeliveryMode_DELIVERY_MODE_EXCLUSIVE

	merged := make(chan *pb.Message, 64)
	var mu sync.Mutex
	subs := make(map[string]*channelSub)
	stopFuncs := make(map[string]context.CancelFunc)

	addChannel := func(rawName string) error {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := subs[rawName]; ok {
			return nil
		}
		ch := namespacedKey(ns, rawName)
		sub, err := s.broker.Subscribe(ch)
		if err != nil {
			return err
		}
		cs := &channelSub{sub: sub, channel: ch, rawName: rawName}
		subs[rawName] = cs

		fanCtx, cancel := context.WithCancel(stream.Context())
		stopFuncs[rawName] = cancel

		go func() {
			for {
				select {
				case msg, ok := <-sub.C:
					if !ok {
						return
					}
					pbMsg := &pb.Message{
						Id:              msg.ID,
						Channel:         rawName,
						Data:            msg.Data,
						DeliveryAttempt: msg.DeliveryAttempt,
					}
					select {
					case merged <- pbMsg:
					case <-fanCtx.Done():
						return
					}
				case <-fanCtx.Done():
					return
				}
			}
		}()
		return nil
	}

	removeChannel := func(rawName string) {
		mu.Lock()
		defer mu.Unlock()
		cs, ok := subs[rawName]
		if !ok {
			return
		}
		if cancel, ok := stopFuncs[rawName]; ok {
			cancel()
			delete(stopFuncs, rawName)
		}
		s.broker.Unsubscribe(cs.sub, cs.channel)
		delete(subs, rawName)
	}

	defer func() {
		mu.Lock()
		for name, cancel := range stopFuncs {
			cancel()
			delete(stopFuncs, name)
		}
		for name, cs := range subs {
			s.broker.Unsubscribe(cs.sub, cs.channel)
			delete(subs, name)
		}
		mu.Unlock()
	}()

	for _, ch := range initReq.Channels {
		if err := addChannel(ch); err != nil {
			return status.Errorf(codes.Internal, "subscribe %q: %v", ch, err)
		}
	}

	clientErr := make(chan error, 1)
	clientMsgs := make(chan *pb.SubscribeStream, 16)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				clientErr <- err
				return
			}
			clientMsgs <- msg
		}
	}()

	var ackGate chan struct{}
	if exclusive {
		ackGate = make(chan struct{}, 1)
		ackGate <- struct{}{}
	}

	paused := false

	for {
		if exclusive && !paused {
			select {
			case <-ackGate:
				// Got the token, proceed to read merged
			case cmsg := <-clientMsgs:
				s.handleClientMsg(cmsg, addChannel, removeChannel, &mu, subs, exclusive, ackGate, &paused)
				continue
			case err := <-clientErr:
				if err == io.EOF {
					return nil
				}
				return err
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}

		if paused {
			select {
			case cmsg := <-clientMsgs:
				s.handleClientMsg(cmsg, addChannel, removeChannel, &mu, subs, exclusive, ackGate, &paused)
				continue
			case err := <-clientErr:
				if err == io.EOF {
					return nil
				}
				return err
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}

		select {
		case pbMsg := <-merged:
			if err := stream.Send(pbMsg); err != nil {
				return err
			}
		case cmsg := <-clientMsgs:
			s.handleClientMsg(cmsg, addChannel, removeChannel, &mu, subs, exclusive, ackGate, &paused)
			if exclusive && !paused {
				select {
				case ackGate <- struct{}{}:
				default:
				}
			}
		case err := <-clientErr:
			if err == io.EOF {
				return nil
			}
			return err
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (s *BrokerService) handleClientMsg(
	msg *pb.SubscribeStream,
	addChannel func(string) error,
	removeChannel func(string),
	mu *sync.Mutex,
	subs map[string]*channelSub,
	exclusive bool,
	ackGate chan struct{},
	paused *bool,
) {
	switch action := msg.Action.(type) {
	case *pb.SubscribeStream_Subscribe:
		_ = addChannel(action.Subscribe.Channel)
	case *pb.SubscribeStream_Unsubscribe:
		removeChannel(action.Unsubscribe.Channel)
	case *pb.SubscribeStream_Ack:
		s.broker.Ack(action.Ack.MessageId)
		if exclusive {
			select {
			case ackGate <- struct{}{}:
			default:
			}
		}
	case *pb.SubscribeStream_Pause:
		if exclusive {
			*paused = true
		} else {
			mu.Lock()
			cs, ok := subs[action.Pause.Channel]
			mu.Unlock()
			if ok {
				s.broker.Pause(cs.sub)
			}
		}
	case *pb.SubscribeStream_Resume:
		if exclusive {
			*paused = false
		} else {
			mu.Lock()
			cs, ok := subs[action.Resume.Channel]
			mu.Unlock()
			if ok {
				s.broker.Resume(cs.sub, cs.channel)
			}
		}
	}
}

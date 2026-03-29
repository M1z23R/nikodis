package server

import (
	"context"
	"io"
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

func (s *BrokerService) Subscribe(stream grpc.BidiStreamingServer[pb.SubscribeStream, pb.Message]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	subReq := first.GetSubscribe()
	if subReq == nil {
		return status.Error(codes.InvalidArgument, "first message must be SubscribeRequest")
	}

	ns := extractNamespace(stream.Context())
	ch := namespacedKey(ns, subReq.Channel)

	sub, err := s.broker.Subscribe(ch)
	if err != nil {
		return status.Errorf(codes.Internal, "subscribe: %v", err)
	}
	defer s.broker.Unsubscribe(sub, ch)

	clientErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				clientErr <- err
				return
			}
			switch action := msg.Action.(type) {
			case *pb.SubscribeStream_Ack:
				s.broker.Ack(action.Ack.MessageId)
			case *pb.SubscribeStream_Pause:
				_ = action
				s.broker.Pause(sub)
			case *pb.SubscribeStream_Resume:
				_ = action
				s.broker.Resume(sub, ch)
			}
		}
	}()

	for {
		select {
		case msg := <-sub.C:
			pbMsg := &pb.Message{
				Id:              msg.ID,
				Channel:         subReq.Channel,
				Data:            msg.Data,
				DeliveryAttempt: msg.DeliveryAttempt,
			}
			if err := stream.Send(pbMsg); err != nil {
				return err
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

package client

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/dimitrije/nikodis/pkg/gen"
)

type Client struct {
	conn      *grpc.ClientConn
	cache     pb.CacheServiceClient
	broker    pb.BrokerServiceClient
	namespace string
	token     string
}

func New(addr string, opts ...Option) (*Client, error) {
	o := &options{namespace: "default"}
	for _, opt := range opts {
		opt(o)
	}
	dialOpts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, o.dialOpts...)
	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:      conn,
		cache:     pb.NewCacheServiceClient(conn),
		broker:    pb.NewBrokerServiceClient(conn),
		namespace: o.namespace,
		token:     o.token,
	}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) withMeta(ctx context.Context) context.Context {
	md := metadata.Pairs("x-nikodis-namespace", c.namespace)
	if c.token != "" {
		md.Append("x-nikodis-token", c.token)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	req := &pb.SetRequest{Key: key, Value: value}
	if ttl > 0 {
		secs := uint64(ttl.Seconds())
		req.TtlSeconds = &secs
	}
	_, err := c.cache.Set(c.withMeta(ctx), req)
	return err
}

func (c *Client) Get(ctx context.Context, key string) ([]byte, bool, error) {
	resp, err := c.cache.Get(c.withMeta(ctx), &pb.GetRequest{Key: key})
	if err != nil {
		return nil, false, err
	}
	return resp.Value, resp.Found, nil
}

func (c *Client) Delete(ctx context.Context, key string) (bool, error) {
	resp, err := c.cache.Delete(c.withMeta(ctx), &pb.DeleteRequest{Key: key})
	if err != nil {
		return false, err
	}
	return resp.Deleted, nil
}

func (c *Client) Publish(ctx context.Context, channel string, data []byte, ackTimeout time.Duration) (string, error) {
	req := &pb.PublishRequest{Channel: channel, Data: data}
	if ackTimeout > 0 {
		secs := uint64(ackTimeout.Seconds())
		req.AckTimeoutSeconds = &secs
	}
	resp, err := c.broker.Publish(c.withMeta(ctx), req)
	if err != nil {
		return "", err
	}
	return resp.MessageId, nil
}

func (c *Client) SubscribeExclusive(ctx context.Context, channels ...string) (*Subscription, error) {
	stream, err := c.broker.Subscribe(c.withMeta(ctx))
	if err != nil {
		return nil, err
	}
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Init{
			Init: &pb.SubscribeInit{
				Channels:     channels,
				DeliveryMode: pb.DeliveryMode_DELIVERY_MODE_EXCLUSIVE,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return newExclusiveSubscription(stream), nil
}

func (c *Client) SubscribeIndependent(ctx context.Context, channels ...string) (map[string]*Subscription, error) {
	stream, err := c.broker.Subscribe(c.withMeta(ctx))
	if err != nil {
		return nil, err
	}
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Init{
			Init: &pb.SubscribeInit{
				Channels:     channels,
				DeliveryMode: pb.DeliveryMode_DELIVERY_MODE_INDEPENDENT,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return newIndependentSubscriptions(stream, channels), nil
}

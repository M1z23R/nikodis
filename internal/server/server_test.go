package server

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/dimitrije/nikodis/pkg/gen"

	"github.com/dimitrije/nikodis/internal/broker"
	"github.com/dimitrije/nikodis/internal/cache"
	"github.com/dimitrije/nikodis/internal/config"
	"github.com/dimitrije/nikodis/pkg/client"
)

// ---------------------------------------------------------------------------
// Metadata tests
// ---------------------------------------------------------------------------

func TestExtractNamespace_Default(t *testing.T) {
	ctx := context.Background()
	if ns := extractNamespace(ctx); ns != defaultNS {
		t.Fatalf("expected %q, got %q", defaultNS, ns)
	}
}

func TestExtractNamespace_FromMetadata(t *testing.T) {
	md := metadata.Pairs(namespaceHeader, "prod")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if ns := extractNamespace(ctx); ns != "prod" {
		t.Fatalf("expected %q, got %q", "prod", ns)
	}
}

func TestExtractToken_Empty(t *testing.T) {
	ctx := context.Background()
	if tok := extractToken(ctx); tok != "" {
		t.Fatalf("expected empty, got %q", tok)
	}
}

func TestExtractToken_FromMetadata(t *testing.T) {
	md := metadata.Pairs(tokenHeader, "secret123")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if tok := extractToken(ctx); tok != "secret123" {
		t.Fatalf("expected %q, got %q", "secret123", tok)
	}
}

// ---------------------------------------------------------------------------
// Auth tests
// ---------------------------------------------------------------------------

func TestAuth_NilConfig(t *testing.T) {
	ctx := context.Background()
	if err := authenticate(ctx, nil); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestAuth_ValidToken(t *testing.T) {
	cfg := &config.Config{Namespaces: map[string]string{"default": "tok"}}
	md := metadata.Pairs(tokenHeader, "tok")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if err := authenticate(ctx, cfg); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	cfg := &config.Config{Namespaces: map[string]string{"default": "tok"}}
	md := metadata.Pairs(tokenHeader, "wrong")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	err := authenticate(ctx, cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestAuth_EmptyTokenNoAuth(t *testing.T) {
	cfg := &config.Config{Namespaces: map[string]string{"default": ""}}
	ctx := context.Background()
	if err := authenticate(ctx, cfg); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestAuth_UnknownNamespace(t *testing.T) {
	cfg := &config.Config{Namespaces: map[string]string{"prod": "tok"}}
	// default namespace not in map
	ctx := context.Background()
	err := authenticate(ctx, cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// bufconn helpers
// ---------------------------------------------------------------------------

const bufSize = 1024 * 1024

func startBufconn(t *testing.T, cfg *config.Config) (pb.CacheServiceClient, pb.BrokerServiceClient, *cache.Store, *broker.Broker, func()) {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	store := cache.New(nil)
	b := broker.New(broker.Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 5 * time.Second,
		MaxRedeliveries:   3,
	}, nil)

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(newAuthInterceptor(cfg)),
		grpc.ChainStreamInterceptor(newStreamAuthInterceptor(cfg)),
	}
	srv := grpc.NewServer(opts...)
	pb.RegisterCacheServiceServer(srv, NewCacheService(store))
	pb.RegisterBrokerServiceServer(srv, NewBrokerService(b))

	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		conn.Close()
		srv.GracefulStop()
		store.Close()
		b.Close()
	}

	return pb.NewCacheServiceClient(conn), pb.NewBrokerServiceClient(conn), store, b, cleanup
}

func ctxWithNS(ns, token string) context.Context {
	md := metadata.Pairs(namespaceHeader, ns, tokenHeader, token)
	return metadata.NewOutgoingContext(context.Background(), md)
}

// ---------------------------------------------------------------------------
// Cache service tests
// ---------------------------------------------------------------------------

func TestCacheSetAndGet(t *testing.T) {
	cc, _, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx := context.Background()
	_, err := cc.Set(ctx, &pb.SetRequest{Key: "k1", Value: []byte("v1")})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := cc.Get(ctx, &pb.GetRequest{Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Found || string(resp.Value) != "v1" {
		t.Fatalf("expected found=true value=v1, got found=%v value=%s", resp.Found, resp.Value)
	}
}

func TestCacheGetMissing(t *testing.T) {
	cc, _, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	resp, err := cc.Get(context.Background(), &pb.GetRequest{Key: "nokey"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Found {
		t.Fatal("expected found=false")
	}
}

func TestCacheDelete(t *testing.T) {
	cc, _, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx := context.Background()
	_, _ = cc.Set(ctx, &pb.SetRequest{Key: "dk", Value: []byte("dv")})

	resp, err := cc.Delete(ctx, &pb.DeleteRequest{Key: "dk"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Deleted {
		t.Fatal("expected deleted=true")
	}

	getResp, err := cc.Get(ctx, &pb.GetRequest{Key: "dk"})
	if err != nil {
		t.Fatal(err)
	}
	if getResp.Found {
		t.Fatal("expected found=false after delete")
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	cc, _, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx := context.Background()
	ttl := uint64(1)
	_, err := cc.Set(ctx, &pb.SetRequest{Key: "ttlk", Value: []byte("ttlv"), TtlSeconds: &ttl})
	if err != nil {
		t.Fatal(err)
	}

	// Should be found immediately
	resp, err := cc.Get(ctx, &pb.GetRequest{Key: "ttlk"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Found {
		t.Fatal("expected found=true before expiry")
	}

	// Wait for TTL to expire
	time.Sleep(1500 * time.Millisecond)

	resp, err = cc.Get(ctx, &pb.GetRequest{Key: "ttlk"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Found {
		t.Fatal("expected found=false after TTL expiry")
	}
}

func TestCacheNamespaceIsolation(t *testing.T) {
	cc, _, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctxA := ctxWithNS("nsA", "")
	ctxB := ctxWithNS("nsB", "")

	_, err := cc.Set(ctxA, &pb.SetRequest{Key: "shared", Value: []byte("fromA")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cc.Set(ctxB, &pb.SetRequest{Key: "shared", Value: []byte("fromB")})
	if err != nil {
		t.Fatal(err)
	}

	respA, err := cc.Get(ctxA, &pb.GetRequest{Key: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	respB, err := cc.Get(ctxB, &pb.GetRequest{Key: "shared"})
	if err != nil {
		t.Fatal(err)
	}

	if string(respA.Value) != "fromA" {
		t.Fatalf("nsA: expected fromA, got %s", respA.Value)
	}
	if string(respB.Value) != "fromB" {
		t.Fatalf("nsB: expected fromB, got %s", respB.Value)
	}
}

// ---------------------------------------------------------------------------
// Broker service tests
// ---------------------------------------------------------------------------

func TestBrokerPublishSubscribeAck(t *testing.T) {
	_, bc, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := bc.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Send subscribe request
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Subscribe{
			Subscribe: &pb.SubscribeRequest{Channel: "ch1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Publish a message
	pubResp, err := bc.Publish(ctx, &pb.PublishRequest{Channel: "ch1", Data: []byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if pubResp.MessageId == "" {
		t.Fatal("expected non-empty message ID")
	}

	// Receive the message
	msg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Id != pubResp.MessageId {
		t.Fatalf("expected message ID %s, got %s", pubResp.MessageId, msg.Id)
	}
	if string(msg.Data) != "hello" {
		t.Fatalf("expected data 'hello', got %s", msg.Data)
	}
	if msg.Channel != "ch1" {
		t.Fatalf("expected channel 'ch1', got %s", msg.Channel)
	}

	// Ack the message
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Ack{
			Ack: &pb.Ack{MessageId: msg.Id},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Close the client side
	err = stream.CloseSend()
	if err != nil {
		t.Fatal(err)
	}
}

func TestBrokerOnlyOneSubscriberGetsMessage(t *testing.T) {
	_, bc, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create two subscribers
	stream1, err := bc.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = stream1.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Subscribe{
			Subscribe: &pb.SubscribeRequest{Channel: "ch2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	stream2, err := bc.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = stream2.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Subscribe{
			Subscribe: &pb.SubscribeRequest{Channel: "ch2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Give subscribers time to register
	time.Sleep(100 * time.Millisecond)

	// Publish a message
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch2", Data: []byte("exclusive")})
	if err != nil {
		t.Fatal(err)
	}

	// One subscriber should get it, the other should not (within a short timeout)
	received := 0

	type result struct {
		msg *pb.Message
		err error
	}

	ch1 := make(chan result, 1)
	ch2 := make(chan result, 1)

	go func() {
		m, e := stream1.Recv()
		ch1 <- result{m, e}
	}()
	go func() {
		m, e := stream2.Recv()
		ch2 <- result{m, e}
	}()

	timer := time.After(500 * time.Millisecond)

	for i := 0; i < 2; i++ {
		select {
		case r := <-ch1:
			if r.err == nil {
				received++
			}
		case r := <-ch2:
			if r.err == nil {
				received++
			}
		case <-timer:
			// timeout, stop waiting
			i = 2
		}
	}

	if received != 1 {
		t.Fatalf("expected exactly 1 subscriber to receive the message, got %d", received)
	}
}

func TestBrokerExclusiveMode_BlocksUntilAck(t *testing.T) {
	_, bc, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := bc.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Send init with exclusive mode and one channel
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Init{
			Init: &pb.SubscribeInit{
				Channels:     []string{"ch1"},
				DeliveryMode: pb.DeliveryMode_DELIVERY_MODE_EXCLUSIVE,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	// Publish two messages
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch1", Data: []byte("msg1")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch1", Data: []byte("msg2")})
	if err != nil {
		t.Fatal(err)
	}

	// Should receive first message
	msg1, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if string(msg1.Data) != "msg1" {
		t.Fatalf("expected msg1, got %s", msg1.Data)
	}

	// Second message should NOT arrive yet (exclusive mode blocks)
	recvCh := make(chan *pb.Message, 1)
	go func() {
		m, err := stream.Recv()
		if err == nil {
			recvCh <- m
		}
	}()

	select {
	case <-recvCh:
		t.Fatal("received second message before acking first — exclusive mode violated")
	case <-time.After(300 * time.Millisecond):
		// Good — blocked as expected
	}

	// Ack first message — should unblock second
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Ack{
			Ack: &pb.Ack{MessageId: msg1.Id},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg2 := <-recvCh:
		if string(msg2.Data) != "msg2" {
			t.Fatalf("expected msg2, got %s", msg2.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for second message after ack")
	}
}

func TestBrokerMultiChannel_ReceivesFromAll(t *testing.T) {
	_, bc, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := bc.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Init{
			Init: &pb.SubscribeInit{
				Channels:     []string{"ch-a", "ch-b"},
				DeliveryMode: pb.DeliveryMode_DELIVERY_MODE_INDEPENDENT,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch-a", Data: []byte("from-a")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch-b", Data: []byte("from-b")})
	if err != nil {
		t.Fatal(err)
	}

	received := map[string]string{}
	for i := 0; i < 2; i++ {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		received[msg.Channel] = string(msg.Data)

		// Ack each message
		err = stream.Send(&pb.SubscribeStream{
			Action: &pb.SubscribeStream_Ack{
				Ack: &pb.Ack{MessageId: msg.Id},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if received["ch-a"] != "from-a" {
		t.Fatalf("expected 'from-a' from ch-a, got %q", received["ch-a"])
	}
	if received["ch-b"] != "from-b" {
		t.Fatalf("expected 'from-b' from ch-b, got %q", received["ch-b"])
	}
}

func TestBrokerExclusiveMode_BlocksAcrossChannels(t *testing.T) {
	_, bc, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := bc.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Init{
			Init: &pb.SubscribeInit{
				Channels:     []string{"ch-x", "ch-y"},
				DeliveryMode: pb.DeliveryMode_DELIVERY_MODE_EXCLUSIVE,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	// Publish to both channels
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch-x", Data: []byte("x-msg")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch-y", Data: []byte("y-msg")})
	if err != nil {
		t.Fatal(err)
	}

	// Receive first message (could be from either channel)
	msg1, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}

	// Second message should be blocked
	recvCh := make(chan *pb.Message, 1)
	go func() {
		m, err := stream.Recv()
		if err == nil {
			recvCh <- m
		}
	}()

	select {
	case <-recvCh:
		t.Fatal("received second message before acking first — exclusive cross-channel violated")
	case <-time.After(300 * time.Millisecond):
		// Good
	}

	// Ack first
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Ack{
			Ack: &pb.Ack{MessageId: msg1.Id},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg2 := <-recvCh:
		if msg2 == nil {
			t.Fatal("expected second message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for second message after ack")
	}
}

// ---------------------------------------------------------------------------
// Full-stack integration test
// ---------------------------------------------------------------------------

func TestIntegration_FullStack(t *testing.T) {
	store := cache.New(nil)
	b := broker.New(broker.Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 5 * time.Second,
		MaxRedeliveries:   3,
	}, nil)
	cfg := &config.Config{Namespaces: map[string]string{
		"testapp": "testtoken",
		"default": "",
	}}

	srv, err := NewServer(store, b, cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	defer func() {
		srv.GracefulStop()
		b.Close()
		store.Close()
	}()

	addr := fmt.Sprintf("localhost:%d", srv.Port())

	t.Run("cache", func(t *testing.T) {
		c, err := client.New(addr, client.WithNamespace("testapp"), client.WithToken("testtoken"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx := context.Background()

		if err := c.Set(ctx, "hello", []byte("world"), 0); err != nil {
			t.Fatal(err)
		}
		val, found, err := c.Get(ctx, "hello")
		if err != nil {
			t.Fatal(err)
		}
		if !found || string(val) != "world" {
			t.Errorf("expected 'world', got found=%v val=%q", found, val)
		}

		deleted, err := c.Delete(ctx, "hello")
		if err != nil {
			t.Fatal(err)
		}
		if !deleted {
			t.Error("expected deleted")
		}
	})

	t.Run("broker", func(t *testing.T) {
		c, err := client.New(addr, client.WithNamespace("testapp"), client.WithToken("testtoken"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx := context.Background()

		sub, err := c.Subscribe(ctx, "tasks")
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()

		time.Sleep(50 * time.Millisecond)

		msgID, err := c.Publish(ctx, "tasks", []byte("do-something"), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if msgID == "" {
			t.Fatal("expected message ID")
		}

		select {
		case msg := <-sub.Messages():
			if string(msg.Data) != "do-something" {
				t.Errorf("expected 'do-something', got %q", msg.Data)
			}
			if err := msg.Ack(); err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	})

	t.Run("auth_rejected", func(t *testing.T) {
		c, err := client.New(addr, client.WithNamespace("testapp"), client.WithToken("wrong"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()

		err = c.Set(context.Background(), "k", []byte("v"), 0)
		if err == nil {
			t.Fatal("expected auth error")
		}
	})

	t.Run("namespace_isolation", func(t *testing.T) {
		c1, _ := client.New(addr)
		defer c1.Close()
		c2, _ := client.New(addr, client.WithNamespace("testapp"), client.WithToken("testtoken"))
		defer c2.Close()
		ctx := context.Background()

		c1.Set(ctx, "shared", []byte("from-default"), 0)
		c2.Set(ctx, "shared", []byte("from-testapp"), 0)

		v1, _, _ := c1.Get(ctx, "shared")
		v2, _, _ := c2.Get(ctx, "shared")

		if string(v1) != "from-default" {
			t.Errorf("default ns: expected 'from-default', got %q", v1)
		}
		if string(v2) != "from-testapp" {
			t.Errorf("testapp ns: expected 'from-testapp', got %q", v2)
		}
	})
}

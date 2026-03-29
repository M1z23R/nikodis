package server

import (
	"context"
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

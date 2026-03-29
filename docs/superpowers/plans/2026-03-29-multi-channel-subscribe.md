# Multi-Channel Subscribe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow subscribers to listen on multiple channels through a single gRPC stream, with exclusive (one-at-a-time) and independent (free-flow) delivery modes.

**Architecture:** The proto gets a new `SubscribeInit` handshake with channels + delivery mode. The server manages a map of channel→subscriber per stream, fans messages into a merged channel, and enforces exclusive blocking. The client exposes two methods — `SubscribeExclusive` and `SubscribeIndependent` — with different return types. The broker layer is unchanged.

**Tech Stack:** Go, gRPC, protobuf

**Spec:** `docs/superpowers/specs/2026-03-29-multi-channel-subscribe-design.md`

---

## File Structure

| Action | File | Responsibility |
|--------|------|---------------|
| Modify | `proto/nikodis.proto` | New messages: `SubscribeInit`, `UnsubscribeRequest`, `DeliveryMode` enum. Updated `SubscribeStream` oneof, `Pause`, `Resume`. |
| Regenerate | `pkg/gen/*.go` | Generated code from proto |
| Modify | `internal/server/broker_service.go` | Multi-channel subscriber management, fan-in, exclusive/independent dispatch |
| Modify | `pkg/client/client.go` | Replace `Subscribe()` with `SubscribeExclusive()` and `SubscribeIndependent()` |
| Modify | `pkg/client/subscription.go` | Support shared stream with demux for independent mode, add `Add()`/`Remove()` for exclusive |
| Modify | `internal/server/server_test.go` | Update existing broker tests for new handshake, add multi-channel tests |
| Modify | `examples/pubsub/main.go` | Update to use `SubscribeExclusive` |

---

### Task 1: Update proto definitions

**Files:**
- Modify: `proto/nikodis.proto:38-76`

- [ ] **Step 1: Replace broker proto messages**

Replace the entire broker message section in `proto/nikodis.proto` (lines 38–76) with:

```protobuf
service BrokerService {
  rpc Publish(PublishRequest) returns (PublishResponse);
  rpc Subscribe(stream SubscribeStream) returns (stream Message);
}

message PublishRequest {
  string channel = 1;
  bytes data = 2;
  optional uint64 ack_timeout_seconds = 3;
}
message PublishResponse {
  string message_id = 1;
}

enum DeliveryMode {
  DELIVERY_MODE_INDEPENDENT = 0;
  DELIVERY_MODE_EXCLUSIVE = 1;
}

message SubscribeInit {
  repeated string channels = 1;
  DeliveryMode delivery_mode = 2;
}

message SubscribeStream {
  oneof action {
    SubscribeInit init = 1;
    SubscribeRequest subscribe = 2;
    UnsubscribeRequest unsubscribe = 3;
    Ack ack = 4;
    Pause pause = 5;
    Resume resume = 6;
  }
}

message SubscribeRequest {
  string channel = 1;
}

message UnsubscribeRequest {
  string channel = 1;
}

message Ack {
  string message_id = 1;
}

message Pause {
  string channel = 1;
}

message Resume {
  string channel = 1;
}

message Message {
  string id = 1;
  string channel = 2;
  bytes data = 3;
  uint32 delivery_attempt = 4;
}
```

- [ ] **Step 2: Regenerate protobuf code**

Run: `make proto`
Expected: `pkg/gen/` files regenerated without errors.

- [ ] **Step 3: Verify it compiles**

Run: `go build ./pkg/gen/...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add proto/nikodis.proto pkg/gen/
git commit -m "Update proto: add SubscribeInit, DeliveryMode, UnsubscribeRequest"
```

---

### Task 2: Rewrite server Subscribe handler for multi-channel support

**Files:**
- Modify: `internal/server/broker_service.go:40-101`

- [ ] **Step 1: Write the failing test — exclusive mode blocks until ack**

Add to `internal/server/server_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestBrokerExclusiveMode_BlocksUntilAck -v -count=1`
Expected: FAIL — compilation error, `SubscribeStream_Init` doesn't exist in the generated code yet, or the server doesn't handle `init`.

- [ ] **Step 3: Write the failing test — multi-channel messages arrive from all channels**

Add to `internal/server/server_test.go`:

```go
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
```

- [ ] **Step 4: Write the failing test — exclusive mode blocks across channels**

Add to `internal/server/server_test.go`:

```go
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
```

- [ ] **Step 5: Run all new tests to verify they fail**

Run: `go test ./internal/server/ -run "TestBrokerExclusiveMode_BlocksUntilAck|TestBrokerMultiChannel_ReceivesFromAll|TestBrokerExclusiveMode_BlocksAcrossChannels" -v -count=1`
Expected: FAIL

- [ ] **Step 6: Implement the new Subscribe handler**

Replace the `Subscribe` method in `internal/server/broker_service.go` (lines 40–101) with:

```go
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

	// Merged channel for fan-in from all subscribers
	merged := make(chan *pb.Message, 64)
	var mu sync.Mutex
	subs := make(map[string]*channelSub)       // rawName -> channelSub
	stopFuncs := make(map[string]context.CancelFunc) // rawName -> cancel

	addChannel := func(rawName string) error {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := subs[rawName]; ok {
			return nil // idempotent
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
			return // idempotent
		}
		if cancel, ok := stopFuncs[rawName]; ok {
			cancel()
			delete(stopFuncs, rawName)
		}
		s.broker.Unsubscribe(cs.sub, cs.channel)
		delete(subs, rawName)
	}

	// Cleanup all on exit
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

	// Subscribe to initial channels
	for _, ch := range initReq.Channels {
		if err := addChannel(ch); err != nil {
			return status.Errorf(codes.Internal, "subscribe %q: %v", ch, err)
		}
	}

	// Client message reader
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

	// Exclusive mode state: when waiting for ack, block merged reads
	var ackGate chan struct{}
	if exclusive {
		ackGate = make(chan struct{}, 1)
		ackGate <- struct{}{} // start unblocked
	}

	paused := false

	for {
		// In exclusive mode, wait for ack gate before reading merged
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
			// When paused, only listen for client messages
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
			// In exclusive mode, don't put the token back — wait for ack
		case cmsg := <-clientMsgs:
			s.handleClientMsg(cmsg, addChannel, removeChannel, &mu, subs, exclusive, ackGate, &paused)
			if exclusive && !paused {
				// Return the ack gate token since we didn't send a message
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
			// Unblock the dispatch loop
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
```

Add `"sync"` to the import block in `broker_service.go` (it already has `"io"`, `"time"`, etc.).

- [ ] **Step 7: Run all three tests**

Run: `go test ./internal/server/ -run "TestBrokerExclusiveMode_BlocksUntilAck|TestBrokerMultiChannel_ReceivesFromAll|TestBrokerExclusiveMode_BlocksAcrossChannels" -v -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/server/broker_service.go internal/server/server_test.go
git commit -m "Rewrite Subscribe handler for multi-channel with exclusive/independent modes"
```

---

### Task 3: Add mid-stream subscribe/unsubscribe test (exclusive mode)

**Files:**
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/server/server_test.go`:

```go
func TestBrokerExclusiveMode_MidStreamAddRemove(t *testing.T) {
	_, bc, _, _, cleanup := startBufconn(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := bc.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Start with one channel
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

	// Add a second channel mid-stream
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Subscribe{
			Subscribe: &pb.SubscribeRequest{Channel: "ch2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	// Publish to the newly added channel
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch2", Data: []byte("from-ch2")})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Channel != "ch2" || string(msg.Data) != "from-ch2" {
		t.Fatalf("expected ch2/from-ch2, got %s/%s", msg.Channel, msg.Data)
	}

	// Ack it
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Ack{
			Ack: &pb.Ack{MessageId: msg.Id},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Remove ch2
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Unsubscribe{
			Unsubscribe: &pb.UnsubscribeRequest{Channel: "ch2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	// Publish to ch1 — should still work
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch1", Data: []byte("from-ch1")})
	if err != nil {
		t.Fatal(err)
	}

	msg, err = stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Channel != "ch1" || string(msg.Data) != "from-ch1" {
		t.Fatalf("expected ch1/from-ch1, got %s/%s", msg.Channel, msg.Data)
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/server/ -run TestBrokerExclusiveMode_MidStreamAddRemove -v -count=1`
Expected: PASS (the handler from Task 2 already supports this)

- [ ] **Step 3: Commit**

```bash
git add internal/server/server_test.go
git commit -m "Add mid-stream subscribe/unsubscribe test for exclusive mode"
```

---

### Task 4: Add pause/resume tests for both modes

**Files:**
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Write the failing test — independent mode per-channel pause**

Add to `internal/server/server_test.go`:

```go
func TestBrokerIndependentMode_PerChannelPause(t *testing.T) {
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

	// Pause ch-a
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Pause{
			Pause: &pb.Pause{Channel: "ch-a"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	// Publish to both
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch-a", Data: []byte("a-msg")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch-b", Data: []byte("b-msg")})
	if err != nil {
		t.Fatal(err)
	}

	// Should only receive from ch-b (ch-a is paused)
	msg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Channel != "ch-b" || string(msg.Data) != "b-msg" {
		t.Fatalf("expected ch-b/b-msg, got %s/%s", msg.Channel, msg.Data)
	}

	// Ack it
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Ack{
			Ack: &pb.Ack{MessageId: msg.Id},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Resume ch-a
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Resume{
			Resume: &pb.Resume{Channel: "ch-a"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now should receive from ch-a
	msg, err = stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Channel != "ch-a" || string(msg.Data) != "a-msg" {
		t.Fatalf("expected ch-a/a-msg, got %s/%s", msg.Channel, msg.Data)
	}
}
```

- [ ] **Step 2: Write the failing test — exclusive mode global pause**

Add to `internal/server/server_test.go`:

```go
func TestBrokerExclusiveMode_GlobalPause(t *testing.T) {
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
				Channels:     []string{"ch1"},
				DeliveryMode: pb.DeliveryMode_DELIVERY_MODE_EXCLUSIVE,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	// Pause globally
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Pause{
			Pause: &pb.Pause{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	// Publish
	_, err = bc.Publish(ctx, &pb.PublishRequest{Channel: "ch1", Data: []byte("paused-msg")})
	if err != nil {
		t.Fatal(err)
	}

	// Should NOT receive (paused)
	recvCh := make(chan *pb.Message, 1)
	go func() {
		m, err := stream.Recv()
		if err == nil {
			recvCh <- m
		}
	}()

	select {
	case <-recvCh:
		t.Fatal("received message while paused")
	case <-time.After(300 * time.Millisecond):
		// Good
	}

	// Resume
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Resume{
			Resume: &pb.Resume{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-recvCh:
		if string(msg.Data) != "paused-msg" {
			t.Fatalf("expected paused-msg, got %s", msg.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message after resume")
	}
}
```

- [ ] **Step 3: Run both tests**

Run: `go test ./internal/server/ -run "TestBrokerIndependentMode_PerChannelPause|TestBrokerExclusiveMode_GlobalPause" -v -count=1`
Expected: PASS (handler from Task 2 supports this)

- [ ] **Step 4: Commit**

```bash
git add internal/server/server_test.go
git commit -m "Add pause/resume tests for independent and exclusive modes"
```

---

### Task 5: Update existing server tests for new handshake

**Files:**
- Modify: `internal/server/server_test.go:294-441`

The existing `TestBrokerPublishSubscribeAck` and `TestBrokerOnlyOneSubscriberGetsMessage` use the old `SubscribeRequest`-as-first-message handshake. Update them to use `SubscribeInit`.

- [ ] **Step 1: Update TestBrokerPublishSubscribeAck**

Replace lines 306–313 (the old subscribe send) with:

```go
	err = stream.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Init{
			Init: &pb.SubscribeInit{
				Channels:     []string{"ch1"},
				DeliveryMode: pb.DeliveryMode_DELIVERY_MODE_EXCLUSIVE,
			},
		},
	})
```

- [ ] **Step 2: Update TestBrokerOnlyOneSubscriberGetsMessage**

Replace the subscribe sends for both stream1 and stream2 (lines 369–376 and 383–390) with the same `SubscribeInit` pattern, using `"ch2"` as the channel:

```go
	err = stream1.Send(&pb.SubscribeStream{
		Action: &pb.SubscribeStream_Init{
			Init: &pb.SubscribeInit{
				Channels:     []string{"ch2"},
				DeliveryMode: pb.DeliveryMode_DELIVERY_MODE_EXCLUSIVE,
			},
		},
	})
```

(Same for stream2.)

- [ ] **Step 3: Run all server tests**

Run: `go test ./internal/server/ -v -count=1`
Expected: ALL PASS

- [ ] **Step 4: Commit**

```bash
git add internal/server/server_test.go
git commit -m "Update existing broker tests to use SubscribeInit handshake"
```

---

### Task 6: Update client library — SubscribeExclusive and SubscribeIndependent

**Files:**
- Modify: `pkg/client/client.go:94-108`
- Modify: `pkg/client/subscription.go`

- [ ] **Step 1: Write the failing test — client SubscribeExclusive**

Add a new file `pkg/client/client_test.go`. This tests against a real server so we need the same bufconn setup. However, the client package can't import `internal/server`, so we test at the integration level. Instead, add to `internal/server/server_test.go`:

```go
func TestClientSubscribeExclusive(t *testing.T) {
	store := cache.New(nil)
	b := broker.New(broker.Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 5 * time.Second,
		MaxRedeliveries:   3,
	}, nil)

	srv, err := NewServer(store, b, nil, 0)
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

	c, err := client.New(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()
	sub, err := c.SubscribeExclusive(ctx, "tasks", "alerts")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	time.Sleep(50 * time.Millisecond)

	_, err = c.Publish(ctx, "tasks", []byte("task-1"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Publish(ctx, "alerts", []byte("alert-1"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Should receive one message
	select {
	case msg := <-sub.Messages():
		if err := msg.Ack(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first message")
	}

	// Should receive second after ack
	select {
	case msg := <-sub.Messages():
		if err := msg.Ack(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for second message")
	}
}
```

- [ ] **Step 2: Write the failing test — client SubscribeIndependent**

Add to `internal/server/server_test.go`:

```go
func TestClientSubscribeIndependent(t *testing.T) {
	store := cache.New(nil)
	b := broker.New(broker.Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 5 * time.Second,
		MaxRedeliveries:   3,
	}, nil)

	srv, err := NewServer(store, b, nil, 0)
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

	c, err := client.New(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()
	subs, err := c.SubscribeIndependent(ctx, "jobs", "logs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, s := range subs {
			s.Close()
		}
	}()

	time.Sleep(50 * time.Millisecond)

	_, err = c.Publish(ctx, "jobs", []byte("job-1"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Publish(ctx, "logs", []byte("log-1"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Each channel's subscription should get its own message
	select {
	case msg := <-subs["jobs"].Messages():
		if string(msg.Data) != "job-1" {
			t.Fatalf("expected job-1, got %s", msg.Data)
		}
		if err := msg.Ack(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for jobs message")
	}

	select {
	case msg := <-subs["logs"].Messages():
		if string(msg.Data) != "log-1" {
			t.Fatalf("expected log-1, got %s", msg.Data)
		}
		if err := msg.Ack(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for logs message")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/server/ -run "TestClientSubscribeExclusive|TestClientSubscribeIndependent" -v -count=1`
Expected: FAIL — `c.SubscribeExclusive` and `c.SubscribeIndependent` don't exist.

- [ ] **Step 4: Implement SubscribeExclusive in client.go**

Replace the `Subscribe` method in `pkg/client/client.go` (lines 94–108) with:

```go
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
```

- [ ] **Step 5: Rewrite subscription.go**

Replace the entire content of `pkg/client/subscription.go` with:

```go
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
	stream    pb.BrokerService_SubscribeClient
	msgCh     chan *Message
	done      chan struct{}
	closeOnce sync.Once
	channel   string // set for independent mode subscriptions, empty for exclusive
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
	s.closeOnce.Do(func() {
		close(s.done)
		s.stream.CloseSend()
	})
	return nil
}

// --- Independent mode ---

func newIndependentSubscriptions(stream pb.BrokerService_SubscribeClient, channels []string) map[string]*Subscription {
	subs := make(map[string]*Subscription, len(channels))
	done := make(chan struct{})
	for _, ch := range channels {
		subs[ch] = &Subscription{
			stream:  stream,
			msgCh:   make(chan *Message, 64),
			done:    done,
			channel: ch,
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
```

- [ ] **Step 6: Run client tests**

Run: `go test ./internal/server/ -run "TestClientSubscribeExclusive|TestClientSubscribeIndependent" -v -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add pkg/client/client.go pkg/client/subscription.go internal/server/server_test.go
git commit -m "Add SubscribeExclusive and SubscribeIndependent client methods"
```

---

### Task 7: Update integration test and examples

**Files:**
- Modify: `internal/server/server_test.go:500-535` (integration broker subtest)
- Modify: `examples/pubsub/main.go`

- [ ] **Step 1: Update integration test broker subtest**

Replace the broker subtest (lines 500–535) to use `SubscribeExclusive`:

```go
	t.Run("broker", func(t *testing.T) {
		c, err := client.New(addr, client.WithNamespace("testapp"), client.WithToken("testtoken"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx := context.Background()

		sub, err := c.SubscribeExclusive(ctx, "tasks")
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
```

- [ ] **Step 2: Update pubsub example**

Replace the `worker` function in `examples/pubsub/main.go` (lines 66–101) to use `SubscribeExclusive`:

```go
func worker(id int, ready chan<- struct{}, done <-chan struct{}) {
	c, err := client.New("localhost:6390")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	sub, err := c.SubscribeExclusive(context.Background(), "work-queue")
	if err != nil {
		log.Fatal(err)
	}
	defer sub.Close()

	fmt.Printf("[worker-%d] subscribed\n", id)
	ready <- struct{}{}

	for {
		select {
		case msg, ok := <-sub.Messages():
			if !ok {
				return
			}
			fmt.Printf("[worker-%d] received %q (attempt=%d)\n", id, msg.Data, msg.DeliveryAttempt)

			// Simulate some work
			time.Sleep(150 * time.Millisecond)

			if err := msg.Ack(); err != nil {
				log.Printf("[worker-%d] ack error: %v", id, err)
			}
			fmt.Printf("[worker-%d] acked %q\n", id, msg.Data)
		case <-done:
			return
		}
	}
}
```

- [ ] **Step 3: Run full test suite**

Run: `go test ./... -v -count=1`
Expected: ALL PASS

- [ ] **Step 4: Commit**

```bash
git add internal/server/server_test.go examples/pubsub/main.go
git commit -m "Update integration test and pubsub example for new subscribe API"
```

---

### Task 8: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update the Client API Summary section**

Replace the Pub/Sub section in the Client API Summary with:

```markdown
// Pub/Sub
msgID, err := c.Publish(ctx, channel, data, ackTimeout)  // ackTimeout 0 = server default

// Exclusive — one message at a time across all channels
sub, err := c.SubscribeExclusive(ctx, "ch1", "ch2", "ch3")
msg := <-sub.Messages()                      // Message{ID, Channel, Data, DeliveryAttempt}
msg.Ack()                                    // unblocks next delivery
sub.Add("ch4")                               // mid-stream add
sub.Remove("ch2")                            // mid-stream remove
sub.Pause()                                  // global pause
sub.Resume()                                 // global resume
sub.Close()

// Independent — per-channel subscriptions
subs, err := c.SubscribeIndependent(ctx, "jobs", "logs")
msg := <-subs["jobs"].Messages()
msg.Ack()
subs["jobs"].Pause()
subs["jobs"].Resume()
```

- [ ] **Step 2: Update the Architecture section**

Update the `broker_service.go` description to mention multi-channel and delivery modes:

```
    broker_service.go        BrokerService gRPC implementation (multi-channel, exclusive/independent modes)
```

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "Update CLAUDE.md for multi-channel subscribe API"
```

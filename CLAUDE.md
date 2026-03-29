# Nikodis

In-memory gRPC cache and message broker server with a Go client library.

## Architecture

```
cmd/nikodis/main.go          Entry point — reads env config, wires engines, starts gRPC server
internal/
  cache/                     Cache engine — in-memory map, TTL, lazy expiry, background reaper
  broker/                    Broker engine — per-channel message queue, consumer groups, at-least-once delivery
    broker.go                Main broker orchestration
    channel.go               Per-channel state (subscribers, pending queue, inflight tracking)
    queue.go                 Message queue implementation
    subscriber.go            Subscriber handle
    message.go               Message types
  server/                    gRPC service layer
    server.go                Server setup and wiring
    cache_service.go         CacheService gRPC implementation
    broker_service.go        BrokerService gRPC implementation (multi-channel, exclusive/independent modes)
    auth.go                  Per-namespace token validation via x-nikodis-token metadata
    metadata.go              gRPC metadata extraction (namespace, token)
  config/                    Namespace/token config loading (JSON/YAML)
  debuglog/                  Optional structured JSON-lines debug logging
pkg/
  client/                    Public client library (what users import)
    client.go                Client struct — New(), Set(), Get(), Delete(), Publish(), SubscribeExclusive(), SubscribeIndependent(), Close()
    options.go               Functional options — WithNamespace(), WithToken(), WithDialOptions()
    subscription.go          Subscription type — Messages(), Pause(), Resume(), Add(), Remove(), Close(); Message type with Ack()
  gen/                       Generated protobuf/gRPC code (do not edit, regenerate with `make proto`)
proto/nikodis.proto          Service definitions — CacheService (Set/Get/Delete), BrokerService (Publish/Subscribe)
examples/
  cache/main.go              Cache usage example
  pubsub/main.go             Pub/sub consumer-group example
```

## Key Design Decisions

- **Namespace isolation**: Multiple apps share one server; cache keys and channels are scoped per namespace
- **Consumer groups**: Multiple subscribers on the same channel get round-robin delivery (not fan-out)
- **At-least-once delivery**: Unacked messages requeue after timeout; dropped after max redeliveries (default 5)
- **Bidirectional streaming**: Subscribe uses a bidirectional gRPC stream for push delivery + client control (pause/resume)
- **No persistence**: Graceful shutdown (SIGINT/SIGTERM) waits for inflight acks, then exits

## Building & Running

```bash
make build          # builds ./bin/nikodis
make proto          # regenerates pkg/gen/ from proto/nikodis.proto
make test           # runs all tests
```

Server listens on port 6390 by default. Config via env vars: NIKODIS_PORT, NIKODIS_MAX_BUFFER_SIZE, NIKODIS_DEFAULT_ACK_TIMEOUT, NIKODIS_MAX_REDELIVERIES, NIKODIS_CONFIG_FILE.

## Client API Summary

```go
c, _ := client.New("localhost:6390", client.WithNamespace("ns"), client.WithToken("tok"))
defer c.Close()

// Cache
c.Set(ctx, key, value, ttl)                  // ttl 0 = no expiry
val, found, err := c.Get(ctx, key)
deleted, err := c.Delete(ctx, key)

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

## Testing

Tests are in `internal/broker/broker_test.go` and `internal/server/` (integration tests using a real gRPC server). Run with `make test` or `go test ./...`.

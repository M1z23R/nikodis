# Nikodis — Design Spec

In-memory cache and message broker server in Go, exposed via gRPC, with a shipped Go client library.

## Requirements

- **Cache**: Get/Set/Delete with optional TTL on keys. Values are arbitrary bytes.
- **Broker**: Publish binary data to arbitrary named channels. Subscribe from multiple clients with consumer-group semantics — exactly one subscriber receives each message. At-least-once delivery with acknowledgment.
- **Single binary**, single instance deployment. In-memory only — nothing survives restart.
- **Go client library** with precompiled protobuf — consumers never touch proto files.

## Configuration

Server config is loaded from a file specified via `NIKODIS_CONFIG_FILE` env var. Supports both JSON and YAML — the loader tries JSON first, falls back to YAML. If no config file is specified, all namespaces are open (no auth).

Example (JSON):
```json
{
  "namespaces": {
    "myapp": "secret-token-abc",
    "otherapp": "secret-token-xyz",
    "default": ""
  }
}
```

Example (YAML):
```yaml
namespaces:
  myapp: "secret-token-abc"
  otherapp: "secret-token-xyz"
  default: ""
```

An empty string or absent token means no auth required for that namespace.

## Authentication

- Per-namespace token-based auth.
- Client sends `x-nikodis-token` gRPC metadata on every call.
- A single gRPC interceptor (unary + stream) validates the token against the namespace's configured value. Mismatch → gRPC `UNAUTHENTICATED` error.
- If no config file is loaded, auth is disabled entirely — all namespaces are open.

## Namespaces

All cache keys and broker channels are scoped to a **namespace**, allowing multiple applications to share one server without interference.

- Client provides a namespace at connection time: `client.New(addr, client.WithNamespace("myapp"))`.
- Namespace is sent as gRPC metadata (`x-nikodis-namespace`) on every RPC call.
- Internally, keys and channels are prefixed with the namespace (e.g., `myapp:key`, `myapp:orders`). This is transparent to clients.
- Default namespace: `"default"` when none is specified.
- Namespaces are created implicitly — no setup required.

## Proto API

Two gRPC services in one proto package. All RPCs read the `x-nikodis-namespace` metadata header for scoping.

### CacheService

```protobuf
service CacheService {
  rpc Set(SetRequest) returns (SetResponse);
  rpc Get(GetRequest) returns (GetResponse);
  rpc Delete(DeleteRequest) returns (DeleteResponse);
}

message SetRequest {
  string key = 1;
  bytes value = 2;
  optional uint64 ttl_seconds = 3; // 0 or absent = no expiry
}
message SetResponse {}

message GetRequest {
  string key = 1;
}
message GetResponse {
  bytes value = 1;
  bool found = 2;
}

message DeleteRequest {
  string key = 1;
}
message DeleteResponse {
  bool deleted = 1;
}
```

### BrokerService

```protobuf
service BrokerService {
  rpc Publish(PublishRequest) returns (PublishResponse);
  rpc Subscribe(stream SubscribeStream) returns (stream Message);
}

message PublishRequest {
  string channel = 1;
  bytes data = 2;
  optional uint64 ack_timeout_seconds = 3; // nil = default 30s
}
message PublishResponse {
  string message_id = 1;
}

message SubscribeStream {
  oneof action {
    SubscribeRequest subscribe = 1;
    Ack ack = 2;
    Pause pause = 3;
    Resume resume = 4;
  }
}

message SubscribeRequest {
  string channel = 1;
}
message Ack {
  string message_id = 1;
}
message Pause {}
message Resume {}

message Message {
  string id = 1;
  string channel = 2;
  bytes data = 3;
  uint32 delivery_attempt = 4;
}
```

## Architecture

Three layers, clean separation.

### 1. Engines (`internal/cache`, `internal/broker`)

Pure Go, no gRPC dependency. Reusable by future admin UI.

**Cache engine (`internal/cache`):**
- In-memory map: `map[string]entry` where entry holds value bytes + expiry timestamp.
- `Set(key, value, ttl)`, `Get(key) (value, found)`, `Delete(key) bool`.
- Expiry: lazy check on `Get` + background reaper goroutine sweeping every second.
- Thread-safe via `sync.RWMutex`.

**Broker engine (`internal/broker`):**
- Channels created lazily on first Publish or Subscribe.
- Each channel maintains:
  - **Pending queue**: messages waiting for delivery.
  - **Inflight set**: messages delivered but not yet acked. Tracked per subscriber with ack deadline.
  - **Subscribers**: connected subscribers with a delivery channel and paused flag.
- **Publish(channel, data, ackTimeout)** → generates unique message ID, enqueues to channel. If subscribers available, delivers immediately to one non-paused subscriber.
- **Subscribe(channel)** → returns a subscriber handle. Server pushes messages to it.
- **Ack(messageID)** → removes from inflight.
- **Pause/Resume** → toggles subscriber's paused flag. Paused subscribers don't receive new messages but inflight ack timers keep running.

**Delivery logic (event-driven, not poll-based):**
- Round-robin among non-paused subscribers.
- Delivery is triggered by events, not a spinning loop:
  - **Publish** → attempt immediate delivery. If no eligible (non-paused) subscriber, message stays in pending queue.
  - **Resume** → triggers drain of pending queue to the resumed subscriber.
  - **New subscriber connects** → triggers drain of pending queue.
  - **Ack timeout requeue** → attempt delivery. If no eligible subscriber, message stays in pending.
- When all subscribers are paused or none are connected, no delivery attempts occur — messages accumulate in the pending queue until an eligible subscriber appears.
- When a message's ack timeout expires: remove from inflight, re-enqueue to pending, increment delivery attempt counter.
- Max redelivery count: 5 (configurable). After max attempts, message is dropped.
- Client disconnect: all inflight messages for that subscriber are immediately re-enqueued.

**Buffer policy:**
- When no subscribers are connected, messages queue in the pending buffer.
- Max buffer size: 10,000 messages per channel (configurable).
- Overflow: oldest messages dropped (ring buffer behavior).

### 2. gRPC Layer (`internal/server`)

Thin adapters calling into the engines.

- `CacheServiceServer` — implements `CacheService`, delegates to cache engine.
- `BrokerServiceServer` — implements `BrokerService`. The `Subscribe` bidi stream handler:
  1. Reads initial `SubscribeRequest` to get channel name.
  2. Registers subscriber with broker engine.
  3. Loop: send messages from subscriber handle to stream; read acks/pause/resume from stream.
  4. On stream close: unregisters subscriber (triggers inflight requeue).

### 3. Entrypoint (`cmd/nikodis`)

- Reads config from environment variables:
  - `NIKODIS_PORT` (default: 6390)
  - `NIKODIS_MAX_BUFFER_SIZE` (default: 10000)
  - `NIKODIS_DEFAULT_ACK_TIMEOUT` (default: 30s)
  - `NIKODIS_MAX_REDELIVERIES` (default: 5)
  - `NIKODIS_DEBUG_LOG` (default: false)
  - `NIKODIS_DEBUG_LOG_DIR` (default: ./logs/)
  - `NIKODIS_DEBUG_LOG_ROTATION` (default: daily, options: daily|hourly)
  - `NIKODIS_ADMIN_UI` (reserved for future use, default: false)
- Creates engine instances, wires gRPC services, starts server.
- Graceful shutdown on SIGINT/SIGTERM: stop accepting new connections, wait for inflight acks (with shutdown timeout), exit.

## Client Library (`pkg/client`)

Wraps generated gRPC stubs. Users never import the `gen` package directly.

```go
c, err := client.New("localhost:6390",
    client.WithNamespace("myapp"),
    client.WithToken("secret-token-abc"),
)

// Cache
err = c.Set(ctx, "key", []byte("value"), 60*time.Second) // with TTL
err = c.Set(ctx, "key", []byte("value"), 0)               // no expiry
val, found, err := c.Get(ctx, "key")
deleted, err := c.Delete(ctx, "key")

// Publish
msgID, err := c.Publish(ctx, "orders", data, 5*time.Minute) // custom ack timeout
msgID, err := c.Publish(ctx, "orders", data, 0)              // default timeout

// Subscribe
sub, err := c.Subscribe(ctx, "orders")
for msg := range sub.Messages() {
    // process msg.Data
    msg.Ack()
}
sub.Pause()
sub.Resume()
sub.Close()
```

- `Subscribe` returns a `Subscription` with a `Messages() <-chan *Message` channel.
- Each `*Message` has `ID`, `Channel`, `Data`, `DeliveryAttempt`, and `Ack()` method.
- `Pause()` / `Resume()` send control messages on the bidi stream.
- `Close()` cleanly closes the stream.
- `client.New` accepts functional options for dial options, timeouts, etc.

## Repo Structure

```
nikodis/
  proto/
    nikodis.proto
  pkg/
    gen/              # committed generated protobuf/gRPC Go code
    client/           # Go client library wrapping gen
  internal/
    cache/            # cache engine
    broker/           # message broker engine
    debuglog/         # structured debug logger
    server/           # gRPC service implementations
  cmd/
    nikodis/          # server binary
  docs/
    superpowers/
      specs/          # this file
```

## Debug Log

Structured event log for debugging message flow. Disabled by default.

- Enabled via `NIKODIS_DEBUG_LOG=true`.
- Writes JSON-lines to files in a configurable directory (`NIKODIS_DEBUG_LOG_DIR`, default: `./logs/`).
- Rotation: `NIKODIS_DEBUG_LOG_ROTATION=daily|hourly` (default: `daily`). File naming: `nikodis-2026-03-29.log` or `nikodis-2026-03-29T14.log`.
- Single log file with a `component` field (`cache` or `broker`) on each entry for filtering.
- Logged events (each with timestamp, component, and relevant IDs):
  - **Cache**: `cache_set`, `cache_get`, `cache_delete`, `cache_expire`
  - **Broker**: `publish`, `deliver`, `ack`, `ack_timeout`, `requeue`, `drop_max_redelivery`, `drop_buffer_overflow`
- Purely observational — no replay, no retention policy beyond filesystem. Log files can be cleaned up externally.
- Implementation: `internal/debuglog` package with a `Logger` interface. Broker engine accepts an optional logger. When nil/disabled, zero overhead (no-op implementation).

## Edge Cases

- **Ack timeout expiry**: message re-enqueued, delivery attempt incremented. After max redeliveries (default 5), message dropped.
- **Buffer overflow**: oldest messages dropped when channel buffer exceeds max size.
- **Client disconnect**: inflight messages for that subscriber immediately re-enqueued.
- **Paused subscriber**: still counts as connected. No new deliveries. Inflight ack timers continue running.
- **Cache TTL=0**: key never expires.
- **Publish to channel with no subscribers**: messages buffered up to max buffer size.
- **Graceful shutdown**: stop accepting connections, wait for inflight with shutdown timeout, then exit.

## Future

- Admin UI: simple local web UI (behind `NIKODIS_ADMIN_UI` env flag) for inspecting cache entries, channel state, inflight messages, and subscriber status. Engines are designed with clean interfaces to support this without going through gRPC.

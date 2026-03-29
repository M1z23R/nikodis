# Multi-Channel Subscribe with Delivery Modes

## Overview

Allow subscribers to listen on multiple channels through a single gRPC stream, with two delivery modes that control how messages are dispatched and how pause/resume behaves.

## Delivery Modes

### Exclusive

One message at a time across all subscribed channels. The server blocks dispatch after sending a message and unblocks after receiving the ack. Pause/resume is global — affects all channels.

Use case: a worker that can only process one task at a time regardless of which channel it came from.

### Independent

Messages flow freely from all channels. Each channel gets its own client-side subscription with its own `Messages()` channel. Pause/resume targets a specific channel.

Use case: a worker that handles different kinds of work in parallel, each channel processed by a separate goroutine.

## Proto Changes

```protobuf
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
  string channel = 1;  // ignored in exclusive mode
}

message Resume {
  string channel = 1;  // ignored in exclusive mode
}

message Message {
  string id = 1;
  string channel = 2;
  bytes data = 3;
  uint32 delivery_attempt = 4;
}
```

The old `SubscribeRequest`-as-first-message handshake is replaced by `SubscribeInit`. This is a breaking change.

## Server Changes

### broker_service.go — Subscribe handler

The handler manages a map of `channel -> *Subscriber` instead of a single subscriber.

**Setup phase:**
1. Receive `SubscribeInit`, extract delivery mode and channel list
2. Create a subscriber per channel via `broker.Subscribe(ch)`
3. Fan all subscriber channels into a single merged channel — one goroutine per subscriber forwards from `sub.C` to a shared `merged chan`

**Dispatch loop — exclusive mode:**
- Read from merged channel, send message to client
- Block — stop reading from merged channel until an `Ack` is received for the sent message
- On ack: unblock, resume reading from merged channel

**Dispatch loop — independent mode:**
- Read from merged channel, send to client freely (no blocking)

**Client message handling (both modes):**
- `SubscribeRequest`: create new subscriber via `broker.Subscribe(ch)`, start new fan-in goroutine
- `UnsubscribeRequest`: call `broker.Unsubscribe(sub, ch)`, stop the fan-in goroutine for that channel
- `Ack`: call `broker.Ack(messageID)`. In exclusive mode, also unblock the dispatch loop.
- `Pause`: in exclusive mode, stop the dispatch loop entirely. In independent mode, call `broker.Pause(sub)` for the named channel's subscriber.
- `Resume`: in exclusive mode, restart the dispatch loop. In independent mode, call `broker.Resume(sub, ch)` for the named channel's subscriber.

**Ack timeout in exclusive mode:** The broker requeues the message after timeout. The dispatch loop unblocks and picks up the next message (could be the requeued one or from another channel).

### Broker layer

No changes. The broker already operates per-channel with `Subscribe()`, `Unsubscribe()`, `Pause()`, `Resume()`, `Ack()`. The server calls these multiple times for multi-channel streams.

## Client Changes

Two separate methods with different return types.

### SubscribeExclusive

```go
sub, err := c.SubscribeExclusive(ctx, "jobs", "alerts", "emails")

msg := <-sub.Messages()  // one at a time, any channel
msg.Ack()                // unblocks next delivery server-side

sub.Add("new-channel")   // mid-stream subscribe
sub.Remove("alerts")     // mid-stream unsubscribe
sub.Pause()              // global pause
sub.Resume()             // global resume
sub.Close()
```

Returns a single `*Subscription`. All channels are multiplexed into one `Messages()` channel. Mid-stream add/remove supported.

### SubscribeIndependent

```go
subs, err := c.SubscribeIndependent(ctx, "jobs", "alerts")

jobsSub := subs["jobs"]
alertsSub := subs["alerts"]

msg := <-jobsSub.Messages()
msg.Ack()
jobsSub.Pause()
jobsSub.Resume()
jobsSub.Close()
```

Returns `map[string]*Subscription`. The client demuxes incoming messages by `msg.Channel` into per-channel subscription objects. Each has its own `Messages()`, `Pause()`, `Resume()`.

Mid-stream add/remove is not supported in independent mode. Close and re-subscribe with the new channel set.

### Subscription type changes

The `Subscription` struct is reused for both modes. In exclusive mode, one instance wraps the whole stream. In independent mode, multiple instances share the stream, each filtered to its channel.

`Pause()` and `Resume()` send the appropriate proto message. In exclusive mode, the channel field is empty (global). In independent mode, it carries the channel name.

## Error Handling

- **Subscribe to a channel already subscribed:** Server ignores (idempotent).
- **Unsubscribe from a channel not subscribed:** Server ignores (idempotent).
- **Ack for unknown message ID:** `broker.Ack()` returns false, no error propagated. Same as today.
- **All channels unsubscribed:** Stream stays open but idle. Client can add channels later or close.
- **Exclusive mode ack timeout:** Broker requeues message, server dispatch loop unblocks.

## Testing

- Multi-channel subscribe — messages arrive from all channels
- Exclusive mode — second message only delivered after first is acked
- Exclusive mode — messages from different channels interleave correctly
- Independent mode — messages flow concurrently from multiple channels
- Mid-stream add/remove channel (exclusive mode)
- Pause/Resume per-channel (independent mode)
- Pause/Resume global (exclusive mode)
- Ack timeout in exclusive mode unblocks dispatch
- Idempotent subscribe/unsubscribe
- Existing single-channel behavior works via SubscribeExclusive with one channel

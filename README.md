# Nikodis

In-memory cache and message broker server, exposed via gRPC with a Go client library.

- **Cache**: Get/Set/Delete with optional TTL
- **Pub/Sub**: Publish/Subscribe with consumer-group semantics and at-least-once delivery

Single binary, single instance, no persistence — data lives only while the server runs.

## Installation

```bash
go get github.com/M1z23R/nikodis
```

## Running the Server

```bash
go build -o nikodis ./cmd/nikodis
./nikodis
```

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `NIKODIS_PORT` | `6390` | gRPC listen port |
| `NIKODIS_MAX_BUFFER_SIZE` | `10000` | Max messages per channel |
| `NIKODIS_DEFAULT_ACK_TIMEOUT` | `30` | Ack timeout in seconds |
| `NIKODIS_MAX_REDELIVERIES` | `5` | Max redelivery attempts |
| `NIKODIS_CONFIG_FILE` | — | Path to namespace auth config (JSON/YAML) |

## Client Usage

### Cache

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/M1z23R/nikodis/pkg/client"
)

func main() {
	c, err := client.New("localhost:6390")
	if err != nil {
		panic(err)
	}
	defer c.Close()

	ctx := context.Background()

	// Set a key with 60s TTL
	c.Set(ctx, "greeting", []byte("hello"), 60*time.Second)

	// Get it back
	val, found, _ := c.Get(ctx, "greeting")
	if found {
		fmt.Println(string(val)) // hello
	}

	// Delete it
	c.Delete(ctx, "greeting")
}
```

### Pub/Sub

```go
package main

import (
	"context"
	"fmt"

	"github.com/M1z23R/nikodis/pkg/client"
)

func main() {
	c, err := client.New("localhost:6390")
	if err != nil {
		panic(err)
	}
	defer c.Close()

	ctx := context.Background()

	// Subscribe
	sub, _ := c.Subscribe(ctx, "events")
	defer sub.Close()

	// Publish
	c.Publish(ctx, "events", []byte("task-1"), 0)

	// Receive
	msg := <-sub.Messages()
	fmt.Printf("got: %s\n", msg.Data)
	msg.Ack()
}
```

### With Namespace & Auth

```go
c, err := client.New("localhost:6390",
	client.WithNamespace("myapp"),
	client.WithToken("secret-token"),
)
```

## Examples

Working examples are in `examples/`:

- `examples/cache/` — Cache operations with TTL
- `examples/pubsub/` — Consumer-group pub/sub with multiple workers

```bash
go run ./examples/cache
go run ./examples/pubsub
```

## License

See [LICENSE](LICENSE).

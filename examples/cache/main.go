package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/dimitrije/nikodis/pkg/client"
)

func main() {
	c, err := client.New("localhost:6390")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()

	// Set a key with no expiry
	if err := c.Set(ctx, "user:1:name", []byte("Alice"), 0); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Set user:1:name = Alice (no expiry)")

	// Set a key with 3 second TTL
	if err := c.Set(ctx, "session:abc", []byte("active"), 3*time.Second); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Set session:abc = active (3s TTL)")

	// Read both keys
	val, found, _ := c.Get(ctx, "user:1:name")
	fmt.Printf("Get user:1:name => found=%v value=%q\n", found, val)

	val, found, _ = c.Get(ctx, "session:abc")
	fmt.Printf("Get session:abc => found=%v value=%q\n", found, val)

	// Wait for expiry
	fmt.Println("\nWaiting 4 seconds for session to expire...")
	time.Sleep(4 * time.Second)

	val, found, _ = c.Get(ctx, "session:abc")
	fmt.Printf("Get session:abc => found=%v value=%q (expired)\n", found, val)

	// Permanent key still there
	val, found, _ = c.Get(ctx, "user:1:name")
	fmt.Printf("Get user:1:name => found=%v value=%q (still alive)\n", found, val)

	// Delete
	deleted, _ := c.Delete(ctx, "user:1:name")
	fmt.Printf("\nDelete user:1:name => deleted=%v\n", deleted)

	_, found, _ = c.Get(ctx, "user:1:name")
	fmt.Printf("Get user:1:name => found=%v (after delete)\n", found)
}

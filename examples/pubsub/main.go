package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/dimitrije/nikodis/pkg/client"
)

func main() {
	var wg sync.WaitGroup

	// Start 2 subscriber workers
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			worker(workerID)
		}(i)
	}

	// Give subscribers time to connect
	time.Sleep(200 * time.Millisecond)

	// Publisher
	wg.Add(1)
	go func() {
		defer wg.Done()
		publisher()
	}()

	wg.Wait()
}

func publisher() {
	c, err := client.New("localhost:6390")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()

	for i := 1; i <= 10; i++ {
		data := fmt.Sprintf("task-%d", i)
		msgID, err := c.Publish(ctx, "work-queue", []byte(data), 10*time.Second)
		if err != nil {
			log.Printf("[publisher] error: %v", err)
			continue
		}
		fmt.Printf("[publisher] published %q (id=%s)\n", data, msgID[:8])
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Println("[publisher] done, waiting for workers to finish...")
	time.Sleep(2 * time.Second)
}

func worker(id int) {
	c, err := client.New("localhost:6390")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	sub, err := c.Subscribe(context.Background(), "work-queue")
	if err != nil {
		log.Fatal(err)
	}
	defer sub.Close()

	for msg := range sub.Messages() {
		fmt.Printf("[worker-%d] received %q (attempt=%d)\n", id, msg.Data, msg.DeliveryAttempt)

		// Simulate some work
		time.Sleep(200 * time.Millisecond)

		if err := msg.Ack(); err != nil {
			log.Printf("[worker-%d] ack error: %v", id, err)
		}
	}
}

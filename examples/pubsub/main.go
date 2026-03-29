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
	ready := make(chan struct{}, 2)
	done := make(chan struct{})

	// Start 2 subscriber workers
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			worker(workerID, ready, done)
		}(i)
	}

	// Wait for both workers to be subscribed
	<-ready
	<-ready

	// Small delay to ensure server-side subscription registration completes.
	// The client.Subscribe returns as soon as the SubscribeRequest is sent,
	// but the server may not have registered the subscriber yet.
	time.Sleep(100 * time.Millisecond)

	fmt.Println("Both workers ready, publishing 10 tasks...")
	fmt.Println()

	// Publisher (inline, no goroutine needed)
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
		time.Sleep(200 * time.Millisecond)
	}

	fmt.Println("[publisher] done, waiting for workers to finish...")

	// Give workers time to drain remaining messages, then signal stop
	time.Sleep(3 * time.Second)
	close(done)
	wg.Wait()
}

func worker(id int, ready chan<- struct{}, done <-chan struct{}) {
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

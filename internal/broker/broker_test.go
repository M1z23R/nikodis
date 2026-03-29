package broker

import (
	"testing"
	"time"
)

// --- Queue tests ---

func TestQueue_PushPop(t *testing.T) {
	q := newQueue(10)
	m1 := &Message{ID: "1"}
	m2 := &Message{ID: "2"}
	q.push(m1)
	q.push(m2)

	got := q.pop()
	if got.ID != "1" {
		t.Errorf("expected msg 1, got %s", got.ID)
	}
	got = q.pop()
	if got.ID != "2" {
		t.Errorf("expected msg 2, got %s", got.ID)
	}
	got = q.pop()
	if got != nil {
		t.Error("expected nil from empty queue")
	}
}

func TestQueue_OverflowDropsOldest(t *testing.T) {
	q := newQueue(2)
	q.push(&Message{ID: "1"})
	q.push(&Message{ID: "2"})
	q.push(&Message{ID: "3"})

	got := q.pop()
	if got.ID != "2" {
		t.Errorf("expected msg 2 after overflow, got %s", got.ID)
	}
	got = q.pop()
	if got.ID != "3" {
		t.Errorf("expected msg 3, got %s", got.ID)
	}
}

func TestQueue_Len(t *testing.T) {
	q := newQueue(10)
	if q.len() != 0 {
		t.Error("expected empty queue")
	}
	q.push(&Message{ID: "1"})
	if q.len() != 1 {
		t.Error("expected len 1")
	}
	q.pop()
	if q.len() != 0 {
		t.Error("expected empty after pop")
	}
}

// --- Channel tests ---

func TestChannel_DeliverToSubscriber(t *testing.T) {
	ch := newChannel(100)
	sub := newSubscriber()
	ch.addSubscriber(sub)

	msg := &Message{ID: "m1", Channel: "test", Data: []byte("hello"), AckTimeout: time.Second, MaxRedeliveries: 5}
	ch.enqueue(msg)

	select {
	case got := <-sub.C:
		if got.ID != "m1" {
			t.Errorf("expected m1, got %s", got.ID)
		}
		if got.DeliveryAttempt != 1 {
			t.Errorf("expected delivery attempt 1, got %d", got.DeliveryAttempt)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestChannel_NoSubscribers_Buffers(t *testing.T) {
	ch := newChannel(100)
	msg := &Message{ID: "m1", Channel: "test", Data: []byte("hello"), AckTimeout: time.Second, MaxRedeliveries: 5}
	ch.enqueue(msg)

	if ch.pending.len() != 1 {
		t.Errorf("expected 1 pending message, got %d", ch.pending.len())
	}
}

func TestChannel_PausedSubscriber_NotDelivered(t *testing.T) {
	ch := newChannel(100)
	sub := newSubscriber()
	ch.addSubscriber(sub)
	sub.pause()

	msg := &Message{ID: "m1", Channel: "test", Data: []byte("hello"), AckTimeout: time.Second, MaxRedeliveries: 5}
	ch.enqueue(msg)

	select {
	case <-sub.C:
		t.Fatal("paused subscriber should not receive messages")
	case <-time.After(50 * time.Millisecond):
	}

	if ch.pending.len() != 1 {
		t.Errorf("expected message to stay in pending, got %d", ch.pending.len())
	}
}

// --- Broker tests ---

func TestBroker_PublishAndSubscribe(t *testing.T) {
	b := New(Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 30 * time.Second,
		MaxRedeliveries:   5,
	}, nil)
	defer b.Close()

	sub, err := b.Subscribe("orders")
	if err != nil {
		t.Fatal(err)
	}

	msgID, err := b.Publish("orders", []byte("order-1"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty message ID")
	}

	select {
	case msg := <-sub.C:
		if string(msg.Data) != "order-1" {
			t.Errorf("expected 'order-1', got %q", msg.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestBroker_OnlyOneSubscriberGetsMessage(t *testing.T) {
	b := New(Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 30 * time.Second,
		MaxRedeliveries:   5,
	}, nil)
	defer b.Close()

	sub1, _ := b.Subscribe("ch")
	sub2, _ := b.Subscribe("ch")

	b.Publish("ch", []byte("data"), 0)

	received := 0
	timeout := time.After(200 * time.Millisecond)
	for {
		select {
		case <-sub1.C:
			received++
		case <-sub2.C:
			received++
		case <-timeout:
			if received != 1 {
				t.Errorf("expected exactly 1 delivery, got %d", received)
			}
			return
		}
	}
}

func TestBroker_Ack(t *testing.T) {
	b := New(Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 5 * time.Second,
		MaxRedeliveries:   5,
	}, nil)
	defer b.Close()

	sub, _ := b.Subscribe("ch")
	b.Publish("ch", []byte("data"), 0)

	msg := <-sub.C
	ok := b.Ack(msg.ID)
	if !ok {
		t.Error("expected ack to succeed")
	}
	ok = b.Ack(msg.ID)
	if ok {
		t.Error("expected double ack to return false")
	}
}

func TestBroker_Unsubscribe_RequeuesInflight(t *testing.T) {
	b := New(Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 30 * time.Second,
		MaxRedeliveries:   5,
	}, nil)
	defer b.Close()

	sub1, _ := b.Subscribe("ch")
	b.Publish("ch", []byte("data"), 0)
	<-sub1.C

	sub2, _ := b.Subscribe("ch")
	b.Unsubscribe(sub1, "ch")

	select {
	case msg := <-sub2.C:
		if msg.DeliveryAttempt != 2 {
			t.Errorf("expected delivery attempt 2, got %d", msg.DeliveryAttempt)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for redelivered message")
	}
}

func TestBroker_PauseResume(t *testing.T) {
	b := New(Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 30 * time.Second,
		MaxRedeliveries:   5,
	}, nil)
	defer b.Close()

	sub, _ := b.Subscribe("ch")
	b.Pause(sub)

	b.Publish("ch", []byte("data"), 0)

	select {
	case <-sub.C:
		t.Fatal("should not receive while paused")
	case <-time.After(100 * time.Millisecond):
	}

	b.Resume(sub, "ch")

	select {
	case msg := <-sub.C:
		if string(msg.Data) != "data" {
			t.Errorf("expected 'data', got %q", msg.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout after resume")
	}
}

func TestBroker_AckTimeout_Redelivers(t *testing.T) {
	b := New(Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 100 * time.Millisecond,
		MaxRedeliveries:   5,
	}, nil)
	defer b.Close()

	sub, _ := b.Subscribe("ch")
	b.Publish("ch", []byte("data"), 100*time.Millisecond)

	msg1 := <-sub.C
	if msg1.DeliveryAttempt != 1 {
		t.Fatalf("expected attempt 1, got %d", msg1.DeliveryAttempt)
	}

	select {
	case msg2 := <-sub.C:
		if msg2.ID != msg1.ID {
			t.Error("expected same message ID on redelivery")
		}
		if msg2.DeliveryAttempt != 2 {
			t.Errorf("expected attempt 2, got %d", msg2.DeliveryAttempt)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for redelivery")
	}
}

func TestBroker_MaxRedeliveries_DropsMessage(t *testing.T) {
	b := New(Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 50 * time.Millisecond,
		MaxRedeliveries:   2,
	}, nil)
	defer b.Close()

	sub, _ := b.Subscribe("ch")
	b.Publish("ch", []byte("data"), 50*time.Millisecond)

	<-sub.C
	select {
	case <-sub.C:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for redelivery")
	}

	select {
	case <-sub.C:
		t.Fatal("should not receive after max redeliveries")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBroker_BuffersWhenNoSubscribers(t *testing.T) {
	b := New(Config{
		MaxBufferSize:     100,
		DefaultAckTimeout: 30 * time.Second,
		MaxRedeliveries:   5,
	}, nil)
	defer b.Close()

	b.Publish("ch", []byte("msg1"), 0)
	b.Publish("ch", []byte("msg2"), 0)

	sub, _ := b.Subscribe("ch")

	for _, expected := range []string{"msg1", "msg2"} {
		select {
		case msg := <-sub.C:
			if string(msg.Data) != expected {
				t.Errorf("expected %q, got %q", expected, msg.Data)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for %s", expected)
		}
	}
}

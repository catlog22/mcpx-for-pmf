package observation

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestAsyncRecorderEnqueueDoesNotBlockOnSlowRecord(t *testing.T) {
	var started atomic.Int32
	var finished atomic.Int32
	block := make(chan struct{})
	rec := NewAsyncRecorder(8, func(ctx context.Context, event Event) error {
		started.Add(1)
		select {
		case <-block:
		case <-ctx.Done():
		}
		finished.Add(1)
		return nil
	})

	// Fill one in-flight + buffer without waiting for slow record.
	deadline := time.Now().Add(200 * time.Millisecond)
	for i := 0; i < 8; i++ {
		if time.Now().After(deadline) {
			t.Fatal("Enqueue blocked on slow recorder")
		}
		if !rec.Enqueue(Event{Type: TypeToolCompleted, Tool: "t", Workspace: "w"}) {
			// queue may fill after worker takes one; non-blocking is enough
			break
		}
	}
	// Must return promptly even while record is blocked.
	_ = rec.Enqueue(Event{Type: TypeToolStarted, Tool: "t2", Workspace: "w"})
	close(block)
	rec.Close(2 * time.Second)
	if finished.Load() == 0 && started.Load() == 0 {
		t.Fatal("worker never processed events")
	}
}

func TestAsyncRecorderDropsWhenFull(t *testing.T) {
	block := make(chan struct{})
	rec := NewAsyncRecorder(1, func(ctx context.Context, event Event) error {
		<-block
		return nil
	})
	// First event may be taken by worker or sit in buffer.
	_ = rec.Enqueue(Event{Type: TypeToolStarted, Workspace: "w"})
	// Fill buffer.
	_ = rec.Enqueue(Event{Type: TypeToolStarted, Workspace: "w"})
	// Further enqueue should drop rather than block.
	done := make(chan bool, 1)
	go func() {
		done <- rec.Enqueue(Event{Type: TypeToolCompleted, Workspace: "w"})
	}()
	select {
	case <-done:
		// returned (true or false) without hanging
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Enqueue blocked when queue full")
	}
	close(block)
	rec.Close(time.Second)
}

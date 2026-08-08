package observation

import (
	"context"
	"sync"
	"time"

	"mcpx/internal/logging"
)

const (
	defaultAsyncBuffer = 256
	defaultAsyncDrain  = 2 * time.Second
	// Slightly above observationWriteTimeout; request path never waits here.
	asyncWriteTimeout = 5 * time.Second
)

// AsyncRecorder writes observation events off the tools/call hot path.
// Enqueue never waits on Store.Append; overflow drops with a log line.
type AsyncRecorder struct {
	ch     chan Event
	record func(context.Context, Event) error
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
}

// NewAsyncRecorder starts a single worker. record is typically bridge.Record.
func NewAsyncRecorder(buffer int, record func(context.Context, Event) error) *AsyncRecorder {
	if buffer <= 0 {
		buffer = defaultAsyncBuffer
	}
	if record == nil {
		record = func(context.Context, Event) error { return nil }
	}
	a := &AsyncRecorder{
		ch:     make(chan Event, buffer),
		record: record,
	}
	a.wg.Add(1)
	go a.loop()
	return a
}

// Enqueue queues an event without blocking tools/call. Returns false if dropped.
func (a *AsyncRecorder) Enqueue(event Event) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	select {
	case a.ch <- event:
		return true
	default:
		fields := []any{"type", event.Type, "tool", event.Tool, "workspace", event.Workspace}
		if event.Type == TypeCommandOutput && event.OperationID != "" {
			fields = append(fields, "operation_id", event.OperationID, "recovery", "observe(view=logs)")
		}
		logging.With("component", "workspace_observer").Error("observation queue full; dropping event; durable task log remains the recovery source", fields...)
		return false
	}
}

func (a *AsyncRecorder) loop() {
	defer a.wg.Done()
	for event := range a.ch {
		// Record is best-effort (publish + optional persist). Never let a slow
		// SQLite write stall the whole observation pipeline for long.
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					logging.With("component", "workspace_observer").Error("observation recorder panic recovered", "type", event.Type, "tool", event.Tool, "panic", recovered)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), asyncWriteTimeout)
			_ = a.record(ctx, event)
			cancel()
		}()
	}
}

// Close stops accepting events, closes the queue so the worker drains remaining
// items, and waits up to timeout for the worker to finish.
func (a *AsyncRecorder) Close(timeout time.Duration) {
	if a == nil {
		return
	}
	if timeout <= 0 {
		timeout = defaultAsyncDrain
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	close(a.ch)
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		logging.With("component", "workspace_observer").Error("async observation drain timed out")
	}
}

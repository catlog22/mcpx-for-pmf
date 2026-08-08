package observation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client is a read-only observer client. It never sends anything except the
// initial subscription frame and automatically reconnects from lastSequence.
type Client struct {
	Path         string
	RetryDelay   time.Duration
	HistoryLimit int
}

func NewClient(path string) *Client {
	return &Client{Path: path, RetryDelay: 250 * time.Millisecond, HistoryLimit: DefaultHistory}
}

// Run invokes onFrame for protocol frames until ctx is cancelled or a fatal
// server error is received.
func (c *Client) Run(ctx context.Context, request SubscribeRequest, onFrame func(Frame) error) error {
	if c == nil || c.Path == "" {
		return fmt.Errorf("observer client path is required")
	}
	if onFrame == nil {
		return fmt.Errorf("observer client frame handler is required")
	}
	if request.Type == "" {
		request.Type = "subscribe"
	}
	if request.HistoryLimit <= 0 {
		request.HistoryLimit = c.HistoryLimit
	}
	if request.HistoryLimit <= 0 {
		request.HistoryLimit = DefaultHistory
	}
	if request.HistoryLimit > MaxObserverHistory {
		request.HistoryLimit = MaxObserverHistory
	}
	delay := c.RetryDelay
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	lastSequence := request.AfterSequence
	connected := false
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		request.AfterSequence = lastSequence
		conn, err := dialObserverSocket(ctx, c.Path)
		if err != nil {
			if !connected {
				return fmt.Errorf("connect observer socket: %w", err)
			}
			if err := waitRetry(ctx, delay); err != nil {
				return nil
			}
			continue
		}
		connected = true
		reconnect, err := c.readConnection(ctx, conn, request, &lastSequence, onFrame)
		_ = conn.Close()
		if err != nil {
			return err
		}
		if !reconnect {
			return nil
		}
		if err := waitRetry(ctx, delay); err != nil {
			return nil
		}
	}
}

func (c *Client) readConnection(ctx context.Context, conn net.Conn, request SubscribeRequest, lastSequence *int64, onFrame func(Frame) error) (bool, error) {
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(request); err != nil {
		return true, err
	}

	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	defer close(closed)

	decoder := json.NewDecoder(bufio.NewReader(conn))
	for {
		var frame Frame
		if err := decoder.Decode(&frame); err != nil {
			if ctx.Err() != nil {
				return false, nil
			}
			return true, nil
		}
		switch frame.Type {
		case "event":
			if frame.Event == nil || frame.Event.Sequence <= *lastSequence {
				continue
			}
			if err := onFrame(frame); err != nil {
				return false, err
			}
			*lastSequence = frame.Event.Sequence
		case "gap":
			if err := onFrame(frame); err != nil {
				return false, err
			}
			return true, nil
		case "error":
			return false, fmt.Errorf("observer server %s: %s", frame.Code, frame.Message)
		default:
			if err := onFrame(frame); err != nil {
				return false, err
			}
		}
	}
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

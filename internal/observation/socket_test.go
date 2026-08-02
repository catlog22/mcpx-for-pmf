package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"
)

func startTestSocket(t *testing.T) (*Store, *Broker, string) {
	t.Helper()
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	broker := NewBroker()
	path := fmt.Sprintf("/tmp/mcpx-observer-%d.sock", time.Now().UnixNano())
	server := NewSocketServer(path, store, broker, func(workspace string) bool { return workspace == "mcpx" })
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		broker.Close()
	})
	return store, broker, path
}

func TestSocketServerReplaysRecentAndPushesLiveEvent(t *testing.T) {
	store, broker, path := startTestSocket(t)
	for _, summary := range []string{"old-1", "old-2", "old-3"} {
		if _, err := store.Append(context.Background(), Event{Workspace: "mcpx", Type: TypeObserverNotice, Summary: summary}); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := dialObserverSocket(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	if err := encoder.Encode(SubscribeRequest{Type: "subscribe", Workspace: "mcpx", HistoryLimit: 2}); err != nil {
		t.Fatal(err)
	}
	var hello Frame
	if err := decoder.Decode(&hello); err != nil || hello.Type != "hello" {
		t.Fatalf("hello=%+v err=%v", hello, err)
	}
	var first, second Frame
	if err := decoder.Decode(&first); err != nil || first.Event == nil || first.Event.Summary != "old-2" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if err := decoder.Decode(&second); err != nil || second.Event == nil || second.Event.Summary != "old-3" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	liveEvent, err := store.Append(context.Background(), Event{Workspace: "mcpx", Type: TypeObserverNotice, Summary: "live"})
	if err != nil {
		t.Fatal(err)
	}
	broker.Publish(liveEvent)
	var live Frame
	if err := decoder.Decode(&live); err != nil || live.Event == nil || live.Event.Summary != "live" {
		t.Fatalf("live=%+v err=%v", live, err)
	}
}

func TestSocketServerRejectsUnknownWorkspace(t *testing.T) {
	_, _, path := startTestSocket(t)
	conn, err := dialObserverSocket(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	if err := encoder.Encode(SubscribeRequest{Type: "subscribe", Workspace: "missing"}); err != nil {
		t.Fatal(err)
	}
	var frame Frame
	if err := decoder.Decode(&frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "error" || frame.Code != "WORKSPACE_NOT_FOUND" {
		t.Fatalf("frame=%+v", frame)
	}
}

func TestClientDeduplicatesEventSequences(t *testing.T) {
	serverConn, clientConn := netPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer serverConn.Close()
		decoder := json.NewDecoder(serverConn)
		encoder := json.NewEncoder(serverConn)
		var request SubscribeRequest
		_ = decoder.Decode(&request)
		_ = encoder.Encode(Frame{Type: "hello", Workspace: "mcpx"})
		_ = encoder.Encode(Frame{Type: "event", Event: &Event{Sequence: 1, Workspace: "mcpx"}})
		_ = encoder.Encode(Frame{Type: "event", Event: &Event{Sequence: 1, Workspace: "mcpx"}})
		_ = encoder.Encode(Frame{Type: "event", Event: &Event{Sequence: 2, Workspace: "mcpx"}})
	}()

	client := &Client{}
	last := int64(0)
	count := 0
	_, err := client.readConnection(ctx, clientConn, SubscribeRequest{Type: "subscribe", Workspace: "mcpx"}, &last, func(frame Frame) error {
		if frame.Type == "event" {
			count++
			if frame.Event.Sequence == 2 {
				cancel()
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || last != 2 {
		t.Fatalf("count=%d last=%d", count, last)
	}
}

func TestClientReportsUnavailableSocketImmediately(t *testing.T) {
	client := NewClient("/tmp/mcpx-observer-does-not-exist.sock")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Run(ctx, SubscribeRequest{Workspace: "mcpx"}, func(Frame) error { return nil }); err == nil {
		t.Fatal("unavailable observer socket should return an error")
	}
}

func netPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	return server, client
}

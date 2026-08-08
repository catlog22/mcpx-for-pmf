package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// SubscribeRequest is the only request accepted by the observer transport.
type SubscribeRequest struct {
	Type          string `json:"type"`
	Workspace     string `json:"workspace"`
	AfterSequence int64  `json:"after_sequence"`
	HistoryLimit  int    `json:"history_limit"`
	Format        string `json:"format,omitempty"`
}

// Frame is a JSON Lines protocol frame sent by SocketServer.
type Frame struct {
	Type       string `json:"type"`
	Workspace  string `json:"workspace,omitempty"`
	ObserverID string `json:"observer_id,omitempty"`
	Sequence   int64  `json:"sequence,omitempty"`
	Event      *Event `json:"event,omitempty"`
	Gap        *Gap   `json:"gap,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// WorkspaceValidator controls which registered workspaces can be observed.
type WorkspaceValidator func(string) bool

// SocketServer serves a read-only local observation stream.
type SocketServer struct {
	path     string
	store    *Store
	broker   *Broker
	validate WorkspaceValidator

	mu          sync.Mutex
	listener    net.Listener
	connections map[net.Conn]struct{}
	closed      bool
	done        chan struct{}
	wg          sync.WaitGroup
}

func NewSocketServer(path string, store *Store, broker *Broker, validate WorkspaceValidator) *SocketServer {
	return &SocketServer{
		path:        path,
		store:       store,
		broker:      broker,
		validate:    validate,
		connections: map[net.Conn]struct{}{},
	}
}

func SocketPath(home string) string {
	return observerSocketPath(home)
}

func (s *SocketServer) Start() error {
	if s == nil || s.store == nil || s.broker == nil {
		return fmt.Errorf("observer socket requires store and broker")
	}
	listener, err := listenObserverSocket(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.listener != nil {
		s.mu.Unlock()
		_ = listener.Close()
		return fmt.Errorf("observer socket is already running")
	}
	s.listener = listener
	s.done = make(chan struct{})
	s.closed = false
	s.mu.Unlock()

	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

func (s *SocketServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.connections[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serveConnection(conn)
	}
}

func (s *SocketServer) serveConnection(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)
	var request SubscribeRequest
	if err := decoder.Decode(&request); err != nil {
		_ = sendFrame(encoder, Frame{Type: "error", Code: "INVALID_REQUEST", Message: "invalid observer subscription"})
		return
	}
	request.Workspace = strings.TrimSpace(request.Workspace)
	if request.Type != "subscribe" || request.Workspace == "" {
		_ = sendFrame(encoder, Frame{Type: "error", Code: "INVALID_REQUEST", Message: "observer subscription requires type=subscribe and workspace"})
		return
	}
	if s.validate != nil && !s.validate(request.Workspace) {
		_ = sendFrame(encoder, Frame{Type: "error", Code: "WORKSPACE_NOT_FOUND", Message: fmt.Sprintf("workspace %q is not registered", request.Workspace)})
		return
	}
	if request.HistoryLimit <= 0 {
		request.HistoryLimit = DefaultHistory
	}
	if request.HistoryLimit > MaxObserverHistory {
		request.HistoryLimit = MaxObserverHistory
	}

	subscription := s.broker.Subscribe(request.Workspace, DefaultBuffer)
	defer subscription.Close()
	observerID := fmt.Sprintf("obs_%d", time.Now().UTC().UnixNano())
	if err := sendFrame(encoder, Frame{Type: "hello", Workspace: request.Workspace, ObserverID: observerID}); err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	history, err := s.store.History(ctx, request.Workspace, request.AfterSequence, request.HistoryLimit)
	if err != nil {
		_ = sendFrame(encoder, Frame{Type: "error", Code: "HISTORY_READ_FAILED", Message: err.Error()})
		return
	}
	lastSequence := request.AfterSequence
	for _, event := range history {
		if event.Sequence <= lastSequence {
			continue
		}
		if err := sendFrame(encoder, Frame{Type: "event", Workspace: request.Workspace, Sequence: event.Sequence, Event: &event}); err != nil {
			return
		}
		lastSequence = event.Sequence
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event, ok := <-subscription.Events:
			if !ok {
				return
			}
			if event.Sequence <= lastSequence {
				continue
			}
			if err := sendFrame(encoder, Frame{Type: "event", Workspace: request.Workspace, Sequence: event.Sequence, Event: &event}); err != nil {
				return
			}
			lastSequence = event.Sequence
		case gap, ok := <-subscription.Gaps:
			if !ok {
				return
			}
			if gap.ToSequence <= lastSequence {
				continue
			}
			_ = sendFrame(encoder, Frame{Type: "gap", Workspace: request.Workspace, Gap: &gap})
			return
		case <-heartbeat.C:
			if err := sendFrame(encoder, Frame{Type: "heartbeat", Workspace: request.Workspace, Sequence: lastSequence}); err != nil {
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *SocketServer) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	started := s.listener != nil || s.done != nil
	if s.done != nil {
		close(s.done)
	}
	listener := s.listener
	connections := make([]net.Conn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}
	s.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	s.wg.Wait()
	if !started {
		return nil
	}
	return removeObserverSocket(s.path)
}

func sendFrame(encoder *json.Encoder, frame Frame) error {
	return encoder.Encode(frame)
}

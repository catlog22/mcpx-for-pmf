package observation

import "sync"

type subscriber struct {
	workspace string
	events    chan Event
	gaps      chan Gap
}

// Subscription is a read-only view of events for one workspace.
type Subscription struct {
	Events <-chan Event
	Gaps   <-chan Gap
	Close  func()
}

// Broker distributes already-persisted events to live local observers.
type Broker struct {
	mu          sync.Mutex
	nextID      uint64
	closed      bool
	subscribers map[uint64]*subscriber
}

func NewBroker() *Broker {
	return &Broker{subscribers: map[uint64]*subscriber{}}
}

func (b *Broker) Subscribe(workspace string, buffer int) Subscription {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Subscription{Events: closedEvents(), Gaps: closedGaps(), Close: func() {}}
	}
	b.nextID++
	id := b.nextID
	sub := &subscriber{workspace: workspace, events: make(chan Event, buffer), gaps: make(chan Gap, 1)}
	b.subscribers[id] = sub
	return Subscription{
		Events: sub.events,
		Gaps:   sub.gaps,
		Close: func() {
			b.unsubscribe(id)
		},
	}
}

func (b *Broker) Publish(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, sub := range b.subscribers {
		if sub.workspace != event.Workspace {
			continue
		}
		select {
		case sub.events <- event:
		default:
			// Channel full: force a gap so the observer reloads instead of
			// sitting forever on a stalled live stream with no new frames.
			select {
			case <-sub.gaps:
			default:
			}
			select {
			case sub.gaps <- Gap{FromSequence: event.Sequence, ToSequence: event.Sequence}:
			default:
			}
		}
	}
}

func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, sub := range b.subscribers {
		close(sub.events)
		close(sub.gaps)
		delete(b.subscribers, id)
	}
}

func (b *Broker) unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.subscribers[id]; ok {
		close(sub.events)
		close(sub.gaps)
		delete(b.subscribers, id)
	}
}

func closedEvents() <-chan Event {
	channel := make(chan Event)
	close(channel)
	return channel
}

func closedGaps() <-chan Gap {
	channel := make(chan Gap)
	close(channel)
	return channel
}

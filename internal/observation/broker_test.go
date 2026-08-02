package observation

import (
	"testing"
	"time"
)

func TestBrokerFiltersWorkspace(t *testing.T) {
	broker := NewBroker()
	defer broker.Close()
	mcpx := broker.Subscribe("mcpx", 4)
	other := broker.Subscribe("other", 4)
	defer mcpx.Close()
	defer other.Close()

	broker.Publish(Event{Sequence: 1, Workspace: "mcpx", Type: TypeToolStarted})
	select {
	case event := <-mcpx.Events:
		if event.Sequence != 1 {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("mcpx subscriber did not receive event")
	}
	select {
	case event := <-other.Events:
		t.Fatalf("other workspace received event: %+v", event)
	default:
	}
}

func TestBrokerEmitsGapWhenSubscriberBufferIsFull(t *testing.T) {
	broker := NewBroker()
	defer broker.Close()
	subscription := broker.Subscribe("mcpx", 1)
	defer subscription.Close()

	broker.Publish(Event{Sequence: 1, Workspace: "mcpx", Type: TypeToolStarted})
	broker.Publish(Event{Sequence: 2, Workspace: "mcpx", Type: TypeToolCompleted})
	select {
	case gap := <-subscription.Gaps:
		if gap.FromSequence != 2 || gap.ToSequence != 2 {
			t.Fatalf("gap=%+v", gap)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("full subscriber did not receive gap")
	}
}

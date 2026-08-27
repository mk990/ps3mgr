package events

import (
	"testing"
	"time"
)

func TestBusDeliversAndUnsubscribes(t *testing.T) {
	bus := New()
	stream, unsubscribe := bus.Subscribe(1)
	bus.Publish("games.loaded", 4)
	select {
	case event := <-stream:
		if event.Type != "games.loaded" || event.Payload.(int) != 4 || event.Time.IsZero() {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not delivered")
	}
	unsubscribe()
	if _, ok := <-stream; ok {
		t.Fatal("subscription channel should be closed")
	}
}

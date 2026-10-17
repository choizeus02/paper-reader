package jobs

import (
	"testing"
	"time"
)

func recv(t *testing.T, ch <-chan Event) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(time.Second):
		t.Fatal("no event")
		return Event{}, false
	}
}

func TestHubReplayAndLive(t *testing.T) {
	h := NewHub()
	key := Key{PaperID: "p", PageNo: 1}
	if _, _, _, ok := h.Subscribe(key); ok {
		t.Fatal("no stream yet")
	}
	h.Open(key)
	h.Publish(key, Event{Name: "status", Data: "running"})
	replay, ch, cancel, ok := h.Subscribe(key)
	defer cancel()
	if !ok || len(replay) != 1 || replay[0].Name != "status" {
		t.Fatalf("replay = %+v ok=%v", replay, ok)
	}
	h.Publish(key, Event{Name: "block", Data: 1})
	if e, _ := recv(t, ch); e.Name != "block" {
		t.Fatalf("got %+v", e)
	}
	h.Publish(key, Event{Name: "done"})
	h.Close(key)
	if e, _ := recv(t, ch); e.Name != "done" {
		t.Fatalf("got %+v", e)
	}
	if _, ok := recv(t, ch); ok {
		t.Fatal("channel should be closed after Close")
	}
	if _, _, _, ok := h.Subscribe(key); ok {
		t.Fatal("stream should be gone after Close")
	}
}

func TestHubCancelAndReopen(t *testing.T) {
	h := NewHub()
	key := Key{PaperID: "p", PageNo: 1}
	h.Open(key)
	_, ch1, cancel1, _ := h.Subscribe(key)
	cancel1()
	if _, ok := recv(t, ch1); ok {
		t.Fatal("cancel should close the channel")
	}
	cancel1() // idempotent

	_, ch2, cancel2, _ := h.Subscribe(key)
	defer cancel2()
	h.Publish(key, Event{Name: "old"})
	h.Open(key) // new run replaces the stream
	if e, ok := recv(t, ch2); !ok || e.Name != "old" {
		t.Fatalf("want buffered old event, got %+v %v", e, ok)
	}
	if _, ok := recv(t, ch2); ok {
		t.Fatal("old subscribers are closed on reopen")
	}
	replay, _, cancel3, ok := h.Subscribe(key)
	defer cancel3()
	if !ok || len(replay) != 0 {
		t.Fatalf("reopened stream should start empty: %+v", replay)
	}
	h.Publish(Key{PaperID: "x"}, Event{Name: "ignored"}) // no stream: no panic
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	h := NewHub()
	key := Key{PaperID: "p", PageNo: 1}
	h.Open(key)
	_, ch, cancel, _ := h.Subscribe(key)
	defer cancel()
	for i := 0; i < subBuffer+10; i++ {
		h.Publish(key, Event{Name: "block", Data: i})
	}
	n := 0
	for range ch {
		n++
	}
	if n != subBuffer {
		t.Fatalf("got %d buffered events before close, want %d", n, subBuffer)
	}
}

func TestHubCancelAfterCloseAndTwice(t *testing.T) {
	h := NewHub()
	key := Key{PaperID: "p", PageNo: 1}
	h.Open(key)
	_, ch, cancel, _ := h.Subscribe(key)
	h.Close(key)
	if _, ok := recv(t, ch); ok {
		t.Fatal("channel should be closed after Close")
	}
	cancel() // after Close: must not panic
	cancel() // twice: must not panic

	h.Open(key)
	_, ch2, cancel2, _ := h.Subscribe(key)
	h.Open(key) // reopen closes ch2
	if _, ok := recv(t, ch2); ok {
		t.Fatal("channel should be closed after reopen")
	}
	cancel2()
	cancel2()
}

package jobs

import "sync"

// subBuffer is how far a subscriber may fall behind before it is dropped.
const subBuffer = 256

type Event struct {
	Name string
	Data any
}

type stream struct {
	events []Event
	subs   map[chan Event]struct{}
}

// Hub buffers the events of each active job so late subscribers can replay them.
type Hub struct {
	mu      sync.Mutex
	streams map[Key]*stream
}

func NewHub() *Hub { return &Hub{streams: map[Key]*stream{}} }

// Open starts a fresh stream for k, closing subscribers of any previous one.
func (h *Hub) Open(k Key) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.streams[k]; ok {
		for ch := range old.subs {
			delete(old.subs, ch)
			close(ch)
		}
	}
	h.streams[k] = &stream{subs: map[chan Event]struct{}{}}
}

func (h *Hub) Publish(k Key, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.streams[k]
	if !ok {
		return
	}
	s.events = append(s.events, e)
	for ch := range s.subs {
		select {
		case ch <- e:
		default: // too slow: drop; the client reconnects and replays
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// Close ends the stream for k and closes all subscriber channels.
func (h *Hub) Close(k Key) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.streams[k]
	if !ok {
		return
	}
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
	delete(h.streams, k)
}

func (h *Hub) Subscribe(k Key) ([]Event, <-chan Event, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.streams[k]
	if !ok {
		return nil, nil, func() {}, false
	}
	ch := make(chan Event, subBuffer)
	s.subs[ch] = struct{}{}
	replay := append([]Event(nil), s.events...)
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, live := s.subs[ch]; live {
			delete(s.subs, ch)
			close(ch)
		}
	}
	return replay, ch, cancel, true
}

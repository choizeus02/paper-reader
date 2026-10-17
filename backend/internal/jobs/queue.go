// Package jobs runs page-generation work in the background and fans out
// progress events to subscribers.
package jobs

import (
	"context"
	"log"
	"sync"
	"time"
)

type Key struct {
	PaperID string
	PageNo  int
}

type Priority int

const (
	Low Priority = iota
	High
)

type Handler func(ctx context.Context, k Key) error

// Queue is an in-memory two-level priority queue with a fixed worker pool.
// A key is never queued or running twice at the same time.
type Queue struct {
	workers int
	timeout time.Duration

	mu      sync.Mutex
	high    []Key
	low     []Key
	queued  map[Key]Priority
	running map[Key]bool

	wake chan struct{}
	wg   sync.WaitGroup
}

func NewQueue(workers int, timeout time.Duration) *Queue {
	return &Queue{
		workers: workers,
		timeout: timeout,
		queued:  map[Key]Priority{},
		running: map[Key]bool{},
		wake:    make(chan struct{}, workers),
	}
}

func (q *Queue) Start(ctx context.Context, h Handler) {
	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.loop(ctx, h)
	}
}

// Wait blocks until all workers have exited (after the Start context ends).
func (q *Queue) Wait() { q.wg.Wait() }

func (q *Queue) Enqueue(k Key, p Priority) {
	q.mu.Lock()
	if q.running[k] {
		q.mu.Unlock()
		return
	}
	if cur, ok := q.queued[k]; ok {
		if p > cur {
			q.promoteLocked(k)
		}
		q.mu.Unlock()
		return
	}
	q.queued[k] = p
	if p == High {
		q.high = append(q.high, k)
	} else {
		q.low = append(q.low, k)
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Promote moves a queued low-priority key to the end of the high queue.
func (q *Queue) Promote(k Key) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.promoteLocked(k)
}

func (q *Queue) promoteLocked(k Key) {
	if cur, ok := q.queued[k]; !ok || cur == High {
		return
	}
	for i, x := range q.low {
		if x == k {
			q.low = append(q.low[:i], q.low[i+1:]...)
			break
		}
	}
	q.high = append(q.high, k)
	q.queued[k] = High
}

func (q *Queue) next() (Key, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var k Key
	switch {
	case len(q.high) > 0:
		k, q.high = q.high[0], q.high[1:]
	case len(q.low) > 0:
		k, q.low = q.low[0], q.low[1:]
	default:
		return Key{}, false
	}
	delete(q.queued, k)
	q.running[k] = true
	return k, true
}

func (q *Queue) loop(ctx context.Context, h Handler) {
	defer q.wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		k, ok := q.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			}
			continue
		}
		q.run(ctx, h, k)
	}
}

func (q *Queue) run(ctx context.Context, h Handler, k Key) {
	jctx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	defer func() {
		q.mu.Lock()
		delete(q.running, k)
		q.mu.Unlock()
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("job %s/%d panicked: %v", k.PaperID, k.PageNo, r)
		}
	}()
	if err := h(jctx, k); err != nil {
		log.Printf("job %s/%d failed: %v", k.PaperID, k.PageNo, err)
	}
}

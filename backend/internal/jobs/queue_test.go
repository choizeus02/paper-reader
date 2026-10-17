package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recorder runs jobs one at a time; the first job blocks until release is closed.
type recorder struct {
	mu      sync.Mutex
	order   []Key
	started chan Key
	release chan struct{}
	done    chan struct{}
	want    int
}

func newRecorder(want int) *recorder {
	return &recorder{started: make(chan Key, 16), release: make(chan struct{}), done: make(chan struct{}), want: want}
}

func (r *recorder) handle(ctx context.Context, k Key) error {
	r.started <- k
	<-r.release
	r.mu.Lock()
	r.order = append(r.order, k)
	if len(r.order) == r.want {
		close(r.done)
	}
	r.mu.Unlock()
	return nil
}

func (r *recorder) wait(t *testing.T) []Key {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("jobs did not finish")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Key(nil), r.order...)
}

func k(n int) Key { return Key{PaperID: "p", PageNo: n} }

func startQueue(t *testing.T, h Handler) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := NewQueue(1, time.Second)
	q.Start(ctx, h)
	t.Cleanup(func() { cancel(); q.Wait() })
	return q
}

func TestQueueHighBeforeLow(t *testing.T) {
	r := newRecorder(3)
	q := startQueue(t, r.handle)
	q.Enqueue(k(1), Low) // occupies the single worker
	<-r.started
	q.Enqueue(k(2), Low)
	q.Enqueue(k(3), High)
	close(r.release)
	got := r.wait(t)
	if got[0] != k(1) || got[1] != k(3) || got[2] != k(2) {
		t.Fatalf("order = %v", got)
	}
}

func TestQueueDedup(t *testing.T) {
	r := newRecorder(2)
	q := startQueue(t, r.handle)
	q.Enqueue(k(1), High)
	<-r.started
	q.Enqueue(k(1), High) // running → ignored
	q.Enqueue(k(2), Low)
	q.Enqueue(k(2), Low) // queued → ignored
	close(r.release)
	got := r.wait(t)
	time.Sleep(50 * time.Millisecond) // give a duplicate a chance to run
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) != 2 || got[0] != k(1) || got[1] != k(2) {
		t.Fatalf("order = %v", r.order)
	}
}

func TestQueuePromote(t *testing.T) {
	r := newRecorder(4)
	q := startQueue(t, r.handle)
	q.Enqueue(k(1), Low)
	<-r.started
	q.Enqueue(k(2), Low)
	q.Enqueue(k(3), Low)
	q.Promote(k(3))
	q.Enqueue(k(4), Low)
	q.Enqueue(k(4), High) // higher-priority enqueue also promotes
	q.Promote(k(9))       // unknown key: no-op
	close(r.release)
	got := r.wait(t)
	if got[1] != k(3) || got[2] != k(4) || got[3] != k(2) {
		t.Fatalf("order = %v", got)
	}
}

func TestQueueTimeout(t *testing.T) {
	errs := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	q := NewQueue(1, 50*time.Millisecond)
	q.Start(ctx, func(ctx context.Context, k Key) error {
		<-ctx.Done()
		errs <- ctx.Err()
		return ctx.Err()
	})
	defer func() { cancel(); q.Wait() }()
	q.Enqueue(k(1), High)
	select {
	case err := <-errs:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout not applied")
	}
}

func TestQueueSurvivesPanic(t *testing.T) {
	ran := make(chan Key, 2)
	ctx, cancel := context.WithCancel(context.Background())
	q := NewQueue(1, time.Second)
	q.Start(ctx, func(ctx context.Context, k Key) error {
		ran <- k
		if k.PageNo == 1 {
			panic("boom")
		}
		return nil
	})
	defer func() { cancel(); q.Wait() }()
	q.Enqueue(k(1), High)
	q.Enqueue(k(2), High)
	for i := 0; i < 2; i++ {
		select {
		case <-ran:
		case <-time.After(2 * time.Second):
			t.Fatal("worker died after panic")
		}
	}
}

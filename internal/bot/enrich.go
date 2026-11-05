package bot

import (
	"context"
	"sync"
)

// enrichJob is one piece of background work on a stored message (a media
// description, a transcription). key names the file it works on.
type enrichJob struct {
	key string
	fn  func(ctx context.Context)
}

// enricher runs enrichment jobs in the background, one at a time, so replies
// never wait for them. A reply that needs the same file claims its key first
// (claim/release) so the file is never processed twice.
type enricher struct {
	queue  chan enrichJob
	done   chan struct{}
	mu     sync.Mutex
	active map[string]chan struct{} // keys being processed → closed when done
}

// newEnricher returns an enricher whose queue holds size pending jobs.
func newEnricher(size int) *enricher {
	return &enricher{queue: make(chan enrichJob, size), done: make(chan struct{}), active: map[string]chan struct{}{}}
}

// submit queues fn without blocking; it reports false when the queue is full.
func (e *enricher) submit(key string, fn func(ctx context.Context)) bool {
	select {
	case e.queue <- enrichJob{key: key, fn: fn}:
		return true
	default:
		return false
	}
}

// run serves the queue until ctx is done.
func (e *enricher) run(ctx context.Context) {
	defer close(e.done)
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-e.queue:
			if wait, ok := e.claim(j.key); !ok {
				// The reply path is working on this file right now: wait, then
				// run the job, which finds the result already stored.
				select {
				case <-wait:
				case <-ctx.Done():
					return
				}
				if _, ok = e.claim(j.key); !ok {
					continue
				}
			}
			j.fn(ctx)
			e.release(j.key)
		}
	}
}

// claim marks a key as being processed. If it already is, ok is false and
// wait is closed when that work is done.
func (e *enricher) claim(key string) (wait <-chan struct{}, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ch, busy := e.active[key]; busy {
		return ch, false
	}
	e.active[key] = make(chan struct{})
	return nil, true
}

// release ends a claim and wakes whoever waits for it.
func (e *enricher) release(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ch, ok := e.active[key]; ok {
		close(ch)
		delete(e.active, key)
	}
}

package llm

import (
	"context"
	"sync"
)

// Fake is a scripted Client for tests. Each Chat returns the next queued
// response in order; once the queue is exhausted it keeps returning the last
// one. Every request made is recorded in Calls.
type Fake struct {
	mu        sync.Mutex
	Responses []Response
	Calls     []Request
	calls     int
}

// Queue adds a response to the fake's script.
func (f *Fake) Queue(r Response) *Fake {
	f.Responses = append(f.Responses, r)
	return f
}

// Chat implements Client.
func (f *Fake) Chat(_ context.Context, req Request) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, req)
	if f.calls >= len(f.Responses) {
		return f.Responses[len(f.Responses)-1], nil
	}
	r := f.Responses[f.calls]
	f.calls++
	return r, nil
}

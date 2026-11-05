// Package ratelimit implements a sliding-window rate limiter with a global
// and a per-chat budget.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter enforces a global and a per-chat message budget over a sliding
// 60-second window. It is mutex-protected and has no goroutines.
type Limiter struct {
	mu         sync.Mutex
	globalCap  int
	chatCap    int
	now        func() time.Time
	timestamps map[int64][]time.Time // chatID -> reservation times (global window = union)
	global     []time.Time
}

// New returns a Limiter with the given per-minute budgets. now is injectable
// for tests.
func New(globalPerMinute, chatPerMinute int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		globalCap:  globalPerMinute,
		chatCap:    chatPerMinute,
		now:        now,
		timestamps: make(map[int64][]time.Time),
	}
}

// Reserve atomically takes n slots in BOTH the global and the chat window,
// or none. It reports whether the reservation succeeded.
func (l *Limiter) Reserve(chatID int64, n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 {
		return true
	}
	cutoff := l.now().Add(-time.Minute)
	l.global = prune(l.global, cutoff)
	l.timestamps[chatID] = prune(l.timestamps[chatID], cutoff)
	if len(l.global)+n > l.globalCap || len(l.timestamps[chatID])+n > l.chatCap {
		return false
	}
	t := l.now()
	l.global = append(l.global, makeN(t, n)...)
	l.timestamps[chatID] = append(l.timestamps[chatID], makeN(t, n)...)
	return true
}

// Free reports how many slots are currently available in the global and the
// chat window.
func (l *Limiter) Free(chatID int64) (global, chat int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-time.Minute)
	l.global = prune(l.global, cutoff)
	l.timestamps[chatID] = prune(l.timestamps[chatID], cutoff)
	global = l.globalCap - len(l.global)
	chat = l.chatCap - len(l.timestamps[chatID])
	return
}

// prune drops timestamps at or before cutoff.
func prune(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

// makeN returns n copies of t.
func makeN(t time.Time, n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = t
	}
	return out
}

// ChatCap is the per-chat budget per minute.
func (l *Limiter) ChatCap() int { return l.chatCap }

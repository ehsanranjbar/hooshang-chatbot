package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is an injectable clock whose current time can be advanced.
type fakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.current = f.current.Add(d)
	f.mu.Unlock()
}

func newLimiter(global, chat int, fc *fakeClock) *Limiter {
	return New(global, chat, fc.now)
}

func TestGlobalCap(t *testing.T) {
	fc := &fakeClock{current: time.Now()}
	l := newLimiter(3, 100, fc)
	for i := 0; i < 3; i++ {
		if !l.Reserve(1, 1) {
			t.Fatalf("reservation %d failed within global cap", i+1)
		}
	}
	if l.Reserve(1, 1) {
		t.Fatal("reservation beyond global cap should fail")
	}
	if g, c := l.Free(1); g != 0 || c != 97 {
		t.Errorf("Free = (%d, %d), want (0, 97)", g, c)
	}
}

func TestChatCap(t *testing.T) {
	fc := &fakeClock{current: time.Now()}
	l := newLimiter(100, 3, fc)
	for i := 0; i < 3; i++ {
		if !l.Reserve(1, 1) {
			t.Fatalf("chat 1 reservation %d failed within chat cap", i+1)
		}
	}
	if l.Reserve(1, 1) {
		t.Fatal("reservation beyond chat cap should fail")
	}
	// A different chat is unaffected by chat 1's budget.
	if !l.Reserve(2, 1) {
		t.Fatal("chat 2 should have its own budget")
	}
	if g, c := l.Free(1); g != 96 || c != 0 {
		t.Errorf("Free(1) = (%d, %d), want (96, 0)", g, c)
	}
	if g, c := l.Free(2); g != 96 || c != 2 {
		t.Errorf("Free(2) = (%d, %d), want (96, 2)", g, c)
	}
}

func TestWindowSlides(t *testing.T) {
	fc := &fakeClock{current: time.Now()}
	l := newLimiter(2, 2, fc)
	if !l.Reserve(1, 2) {
		t.Fatal("initial reservation failed")
	}
	if l.Reserve(1, 1) {
		t.Fatal("should be at cap")
	}
	// Just inside the 60s window: still capped.
	fc.advance(59 * time.Second)
	if l.Reserve(1, 1) {
		t.Fatal("should still be capped at 59s")
	}
	// Past the window: slots freed.
	fc.advance(2 * time.Second)
	if !l.Reserve(1, 1) {
		t.Fatal("slots should have slid out of the window")
	}
}

func TestReserveAllOrNothing(t *testing.T) {
	fc := &fakeClock{current: time.Now()}
	l := newLimiter(2, 2, fc)
	if l.Reserve(1, 3) {
		t.Fatal("Reserve(3) should fail against a cap of 2")
	}
	// Nothing was taken.
	if g, c := l.Free(1); g != 2 || c != 2 {
		t.Errorf("after failed Reserve(3) Free = (%d, %d), want (2, 2)", g, c)
	}
	if !l.Reserve(1, 2) {
		t.Fatal("Reserve(2) should succeed")
	}
}

func TestReserveRespectsBothWindows(t *testing.T) {
	fc := &fakeClock{current: time.Now()}
	l := newLimiter(5, 3, fc)
	if !l.Reserve(1, 3) {
		t.Fatal("Reserve(3) failed")
	}
	if l.Reserve(1, 1) {
		t.Fatal("chat window full, Reserve(1) should fail")
	}
	// Chat 2 has chat budget, but only 2 global slots remain… not enough for 3.
	if l.Reserve(2, 3) {
		t.Fatal("global window full, Reserve(3) for chat 2 should fail")
	}
	if !l.Reserve(2, 2) {
		t.Fatal("Reserve(2) for chat 2 should succeed within both windows")
	}
}

func TestFreeDoesNotConsume(t *testing.T) {
	fc := &fakeClock{current: time.Now()}
	l := newLimiter(2, 2, fc)
	l.Reserve(1, 1)
	for i := 0; i < 100; i++ {
		if g, c := l.Free(1); g != 1 || c != 1 {
			t.Fatalf("Free changed state on call %d: (%d, %d)", i, g, c)
			return
		}
	}
}

func TestConcurrentReserve(t *testing.T) {
	fc := &fakeClock{current: time.Now()}
	l := newLimiter(50, 100, fc)
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				l.Reserve(int64(g/2), 1)
			}
		}(g)
	}
	wg.Wait()
	g, _ := l.Free(1)
	if g < 0 || g > 50 {
		t.Errorf("global free = %d, want in [0, 50]", g)
	}
}

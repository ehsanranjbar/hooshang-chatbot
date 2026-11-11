package rag

import (
	"testing"
	"time"
)

func TestDecay(t *testing.T) {
	if d := Decay(0, 30*24*time.Hour); d < 0.99 || d > 1.01 {
		t.Errorf("fresh decay = %v", d)
	}
	if d := Decay(60*24*time.Hour, 30*24*time.Hour); d < 0.77 || d > 0.78 {
		t.Errorf("two half-lives = %v", d)
	}
}

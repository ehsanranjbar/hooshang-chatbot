package rag

import (
	"testing"
	"time"
)

func TestQuantizeAndCosine(t *testing.T) {
	a := Quantize([]float32{1, 0, 0, 0})
	b := Quantize([]float32{0.9, 0.1, 0, 0})
	c := Quantize([]float32{0, 1, 0, 0})
	if s := Cosine(a, a); s < 0.99 || s > 1.01 {
		t.Errorf("self cosine = %v", s)
	}
	if ab, ac := Cosine(a, b), Cosine(a, c); ab < 0.9 || ac > 0.05 {
		t.Errorf("cosines: ab=%v ac=%v", ab, ac)
	}
	if Cosine(a, Quantize([]float32{1, 2})) != 0 {
		t.Error("mismatched dimensions should give 0")
	}
}

func TestIndexSearch(t *testing.T) {
	x := NewIndex()
	x.Put(1, Quantize([]float32{1, 0, 0}))
	x.Put(2, Quantize([]float32{0.8, 0.6, 0}))
	x.Put(3, Quantize([]float32{0, 0, 1}))
	hits := x.Search(Quantize([]float32{1, 0.1, 0}), 2, 0.3, func(id int64) bool { return id == 1 }, nil)
	if len(hits) != 1 || hits[0].ID != 2 {
		t.Fatalf("hits = %+v", hits)
	}
	x.Delete(2)
	if x.Len() != 2 || len(x.Search(Quantize([]float32{0.8, 0.6, 0}), 5, 0.9, nil, nil)) != 0 {
		t.Error("delete did not remove the vector")
	}
	if d := Decay(0, 30*24*time.Hour); d < 0.99 || d > 1.01 {
		t.Errorf("fresh decay = %v", d)
	}
	if d := Decay(60*24*time.Hour, 30*24*time.Hour); d < 0.77 || d > 0.78 {
		t.Errorf("two half-lives = %v", d)
	}
}

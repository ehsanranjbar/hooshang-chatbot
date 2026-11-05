// Package rag is the retrieval layer: vectors quantised to int8, cosine
// similarity, and a per-chat in-memory index that is searched by brute force
// (tens of thousands of rows fit and search in a few milliseconds).
package rag

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Vector is an int8-quantised, L2-normalised embedding. The cosine of two
// vectors is Scale(a)·Scale(b)·Σ Q(a)·Q(b).
type Vector struct {
	Q     []int8
	Scale float32
	Hash  uint64    // of the text it embeds, to skip re-embedding unchanged text
	At    time.Time // when it was embedded
}

// Quantize normalises v and quantises it to int8.
func Quantize(v []float32) Vector {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		norm = 1
	}
	maxAbs := 0.0
	for _, x := range v {
		if a := math.Abs(float64(x)) / norm; a > maxAbs {
			maxAbs = a
		}
	}
	scale := maxAbs / 127
	if scale == 0 {
		scale = 1
	}
	q := make([]int8, len(v))
	for i, x := range v {
		q[i] = int8(math.Round(float64(x) / norm / scale))
	}
	return Vector{Q: q, Scale: float32(scale)}
}

// Cosine is the cosine similarity of two quantised vectors (0 when the
// dimensions differ).
func Cosine(a, b Vector) float32 {
	if len(a.Q) != len(b.Q) || len(a.Q) == 0 {
		return 0
	}
	var dot int64
	for i := range a.Q {
		dot += int64(a.Q[i]) * int64(b.Q[i])
	}
	return a.Scale * b.Scale * float32(dot)
}

// Hit is one search result.
type Hit struct {
	ID    int64
	Score float32
}

// Index holds the vectors of one chat in memory.
type Index struct {
	mu   sync.RWMutex
	ids  []int64
	vecs []Vector
	pos  map[int64]int
	used time.Time
}

// NewIndex returns an empty index.
func NewIndex() *Index { return &Index{pos: map[int64]int{}} }

// Put adds or replaces a vector.
func (x *Index) Put(id int64, v Vector) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if i, ok := x.pos[id]; ok {
		x.vecs[i] = v
		return
	}
	x.pos[id] = len(x.ids)
	x.ids = append(x.ids, id)
	x.vecs = append(x.vecs, v)
}

// Delete removes a vector (a missing id is a no-op).
func (x *Index) Delete(id int64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	i, ok := x.pos[id]
	if !ok {
		return
	}
	last := len(x.ids) - 1
	x.ids[i], x.vecs[i] = x.ids[last], x.vecs[last]
	x.pos[x.ids[i]] = i
	x.ids, x.vecs = x.ids[:last], x.vecs[:last]
	delete(x.pos, id)
}

// Len is the number of vectors.
func (x *Index) Len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.ids)
}

// Search returns the k best hits for q with a cosine of at least min, best
// first; skip drops ids; weight, when set, rescales a score (e.g. by age).
func (x *Index) Search(q Vector, k int, min float32, skip func(int64) bool, weight func(int64, float32) float32) []Hit {
	x.mu.Lock()
	x.used = time.Now()
	x.mu.Unlock()
	x.mu.RLock()
	defer x.mu.RUnlock()
	var hits []Hit
	for i, id := range x.ids {
		if skip != nil && skip(id) {
			continue
		}
		s := Cosine(q, x.vecs[i])
		if s < min {
			continue
		}
		if weight != nil {
			s = weight(id, s)
		}
		hits = append(hits, Hit{ID: id, Score: s})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// LastUsed is when the index was last searched (zero when never).
func (x *Index) LastUsed() time.Time {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.used
}

// Decay is the recency factor of the retrieval score: 0.7 + 0.3·2^(−age/half).
func Decay(age, half time.Duration) float32 {
	if half <= 0 {
		return 1
	}
	return float32(0.7 + 0.3*math.Pow(2, -age.Hours()/half.Hours()))
}

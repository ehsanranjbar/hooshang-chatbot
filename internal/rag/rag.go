// Package rag holds the scoring of retrieval that is not the vector search
// itself, which the vector indexes of the store do.
package rag

import (
	"math"
	"time"
)

// Decay is the recency factor of the retrieval score: 0.7 + 0.3·2^(−age/half).
func Decay(age, half time.Duration) float32 {
	if half <= 0 {
		return 1
	}
	return float32(0.7 + 0.3*math.Pow(2, -age.Hours()/half.Hours()))
}

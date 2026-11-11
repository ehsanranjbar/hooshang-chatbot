package store

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/ehsanranjbar/kvutil/kv/memkv"
)

// unit returns a vector of dims dimensions with a 1 at i.
func unit(dims, i int) []float32 {
	v := make([]float32, dims)
	v[i%dims] = 1
	return v
}

func TestEmbeddingModelChangeWipesVectors(t *testing.T) {
	mem := memkv.New()
	open := func(model string, dims int) *DB {
		t.Helper()
		db, err := OpenWith(mem, Options{Retention: time.Hour, EmbeddingModel: model, EmbeddingDims: dims})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	put := func(db *DB) {
		t.Helper()
		if err := db.PutMsgVector(1, 1, Embedding{Vec: unit(db.msgVectors.vectors.Dims(), 0), At: time.Now()}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	has := func(db *DB) bool {
		_, err := db.MsgVector(1, 1)
		return err == nil
	}

	db := open("a", 8)
	put(db)
	if db = open("a", 8); !has(db) {
		t.Fatal("reopening with the same model wiped the vectors")
	}
	if db = open("", 8); !has(db) {
		t.Fatal("retrieval off wiped the vectors")
	}
	if db = open("b", 8); has(db) {
		t.Fatal("vectors survived a model change")
	}
	put(db)
	if db = open("b", 16); has(db) {
		t.Fatal("vectors survived a change of dimensions")
	}
	if m, _ := db.GetEmbeddingMeta(); m != (EmbeddingMeta{Model: "b", Dims: 16}) {
		t.Errorf("meta = %+v", m)
	}
}

func TestExpiryAndPartitionedSearch(t *testing.T) {
	const dims, chat, other = 16, int64(-100), int64(-200)
	db, err := OpenWith(memkv.New(), Options{Retention: time.Hour, EmbeddingDims: dims})
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	noisy := func(i int) []float32 {
		v := unit(dims, i)
		for k := range v {
			v[k] += 0.05 * float32(r.NormFloat64())
		}
		return v
	}
	// Enough vectors in two chats for the upkeep to make partitions (a
	// partition holds at most 1024 here).
	now := time.Now()
	for id := range int64(1500) {
		c := chat
		if id%2 == 1 {
			c = other
		}
		ttl := time.Hour
		if id < 10 {
			ttl = time.Nanosecond // expired by the time the upkeep runs
		}
		if err := db.PutMsgVector(c, id, Embedding{Vec: noisy(int(id)), At: now.Add(-time.Second)}, ttl); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Upkeep(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := db.MsgIndexStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Partitions < 2 || st.Unplaced != 0 {
		t.Errorf("after the upkeep: %+v", st)
	}
	for id := range int64(10) {
		if _, err := db.MsgVector(chat, id); !errors.Is(err, ErrNotFound) {
			if _, err2 := db.MsgVector(other, id); !errors.Is(err2, ErrNotFound) {
				t.Errorf("vector %d did not expire", id)
			}
		}
	}
	if _, err := db.MsgVector(chat, 100); err != nil {
		t.Errorf("a live vector is gone: %v", err)
	}

	hits, err := db.SearchMessages(chat, unit(dims, 4), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 5 {
		t.Fatalf("hits = %+v", hits)
	}
	for _, h := range hits {
		if h.ID%2 == 1 {
			t.Errorf("a message of another chat was found: %+v", h)
		}
		if h.ID%dims != 4 || h.Score < 0.9 {
			t.Errorf("a far message was found: %+v", h)
		}
	}
}

func TestScoreItems(t *testing.T) {
	const dims = 8
	db, err := OpenWith(memkv.New(), Options{Retention: time.Hour, EmbeddingDims: dims})
	if err != nil {
		t.Fatal(err)
	}
	for i := range uint64(3) {
		if err := db.PutItemVector(1, 7, i, Embedding{Vec: unit(dims, int(i))}); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.PutItemVector(1, 8, 0, Embedding{Vec: unit(dims, 0)}) // another entity
	scores, err := db.ScoreItems(1, 7, unit(dims, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 3 || scores[1] < 0.99 || scores[0] > 0.01 {
		t.Errorf("scores = %v", scores)
	}
	if err := db.DeleteEntityVectors(1, 7); err != nil {
		t.Fatal(err)
	}
	if scores, _ := db.ScoreItems(1, 7, unit(dims, 1)); len(scores) != 0 {
		t.Errorf("scores after delete = %v", scores)
	}
}

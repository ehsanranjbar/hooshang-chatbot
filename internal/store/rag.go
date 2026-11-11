package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/ehsanranjbar/kvutil"
	"github.com/ehsanranjbar/kvutil/codec"
	"github.com/ehsanranjbar/kvutil/field"
	"github.com/ehsanranjbar/kvutil/index"
	"github.com/ehsanranjbar/kvutil/kv"
	extstore "github.com/ehsanranjbar/kvutil/store/ext"
	"github.com/ehsanranjbar/kvutil/upkeep"
	"github.com/ehsanranjbar/kvutil/vector"
)

// Key3 is a three-part key: (chatID, (a, b)).
type Key3 = codec.Pair[int64, codec.Pair[int64, int64]]

// K3 builds a Key3.
func K3(chatID, a, b int64) Key3 { return codec.NewPair(chatID, codec.NewPair(a, b)) }

// Archived is a member message kept for retrieval long after the transcript
// message has expired.
type Archived struct {
	UserID int64
	Name   string
	Text   string
	Date   time.Time
}

// Embedding is the vector of a text: an archived message, a memory item or a
// media description. The vector indexes of its stores search them.
type Embedding struct {
	Chat    int64     // the chat it belongs to (0 for media); a message search is limited to it
	Vec     []float32 // as the model gave it
	Hash    uint64    // of the text it embeds, to skip re-embedding unchanged text
	At      time.Time // when it was embedded
	Expires int64     // unix seconds after which the upkeep deletes it; 0 = never
}

// EmbeddingMeta records which model made the stored vectors; a change wipes
// them when the database is opened.
type EmbeddingMeta struct {
	Model string
	Dims  int
}

// Hit is one search result: a message id or a media key hash, and its cosine
// similarity to the query.
type Hit struct {
	ID    int64
	Score float32
}

var (
	embChat    = field.Path[Embedding, int64]("Chat")
	embExpires = field.Path[Embedding, int64]("Expires")
)

// embeddingStore is a store of embeddings with its vector index and the index
// of their expiry, which the expiry job reads.
type embeddingStore[K any] struct {
	name    string
	store   *extstore.Store[K, Embedding]
	vectors *vector.Index[K, Embedding]
	expiry  *index.FieldIndex[K, Embedding, int64]
}

// newEmbeddingStore defines a store of embeddings of dims dimensions under prefix.
func newEmbeddingStore[K any](name string, prefix []byte, dims int, opts ...vector.Option) *embeddingStore[K] {
	s := &embeddingStore[K]{name: name, store: extstore.New[K, Embedding](prefix)}
	s.vectors = vector.On(s.store, "by_vector", dims, vector.Cosine, func(e *Embedding) []float32 { return e.Vec }, opts...)
	s.expiry = index.On(s.store, "by_expiry", embExpires,
		index.WhereFunc("expires", func(e *Embedding) bool { return e.Expires > 0 }))
	return s
}

// expires is the expiry of something embedded at at that lives for ttl (0 = never).
func expires(at time.Time, ttl time.Duration) int64 {
	if ttl <= 0 {
		return 0
	}
	return at.Add(ttl).Unix()
}

// expiryBatch is how many expired embeddings a step of the expiry job deletes
// when the runner does not say.
const expiryBatch = 256

// expiryJob deletes the embeddings that expired: the stores of embeddings have
// extensions, so their records cannot expire by a TTL of the engine.
type expiryJob[K any] struct {
	s   *embeddingStore[K]
	now func() time.Time
}

// Name implements upkeep.Job.
func (j *expiryJob[K]) Name() string { return fmt.Sprintf("expiry of %q", j.s.name) }

// Step implements upkeep.Job: it deletes a batch of expired embeddings, and
// goes on while there are more.
func (j *expiryJob[K]) Step(_ context.Context, db kv.DB, pass upkeep.Pass) ([]byte, error) {
	batch := pass.Batch
	if batch <= 0 {
		batch = expiryBatch
	}
	now := j.now().Unix()
	var n int
	err := db.Update(func(tx kv.Tx) error {
		n = 0
		it, err := j.s.expiry.Instantiate(tx).Range(kvutil.Between[int64](1, now))
		if err != nil {
			return err
		}
		var keys []K
		for ; it.Valid() && len(keys) < batch; it.Next() {
			k, err := it.Key()
			if err != nil {
				it.Close()
				return err
			}
			keys = append(keys, k)
		}
		it.Close()
		s := j.s.store.Instantiate(tx)
		for _, k := range keys {
			if err := s.Delete(k); err != nil {
				return err
			}
		}
		n = len(keys)
		return nil
	})
	if err != nil || n < batch {
		return nil, err
	}
	return []byte{1}, nil
}

// setTTL writes with a TTL, falling back to a plain Set on engines without it.
func setTTL[K any, V any](s interface {
	Set(K, *V) error
	SetWithTTL(K, *V, time.Duration) error
}, k K, v *V, ttl time.Duration) error {
	if ttl <= 0 {
		return s.Set(k, v)
	}
	err := s.SetWithTTL(k, v, ttl)
	if errors.Is(err, kv.ErrUnsupported) {
		err = s.Set(k, v)
	}
	return err
}

// Archive stores a member message for retrieval (and its by-user index) for ttl.
func (d *DB) Archive(chatID, msgID int64, a Archived, ttl time.Duration) error {
	return d.db.Update(func(tx kv.Tx) error {
		if err := setTTL(d.archive.Instantiate(tx), K(chatID, msgID), &a, ttl); err != nil {
			return err
		}
		var empty struct{}
		return setTTL(d.archiveByUser.Instantiate(tx), K3(chatID, a.UserID, msgID), &empty, ttl)
	})
}

// GetArchived returns an archived message, or ErrNotFound.
func (d *DB) GetArchived(chatID, msgID int64) (*Archived, error) {
	var out *Archived
	err := d.db.View(func(tx kv.Tx) error {
		var err error
		out, err = d.archive.Instantiate(tx).Get(K(chatID, msgID))
		return err
	})
	return out, err
}

// ArchivedBy returns a member's newest archived messages, up to n, oldest first.
func (d *DB) ArchivedBy(chatID, userID int64, n int) ([]Archived, error) {
	var out []Archived
	err := d.db.View(func(tx kv.Tx) error {
		arch := d.archive.Instantiate(tx)
		it, err := d.archiveByUser.Instantiate(tx).ReverseIterator(kvutil.Between(K3(chatID, userID, math.MinInt64), K3(chatID, userID, math.MaxInt64)))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid() && len(out) < n; it.Next() {
			k, err := it.Key()
			if err != nil {
				return err
			}
			a, err := arch.Get(K(chatID, k.Second.Second))
			if errors.Is(err, kv.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			out = append(out, *a)
		}
		return nil
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

// PutMsgVector stores a message's vector for ttl.
func (d *DB) PutMsgVector(chatID, msgID int64, e Embedding, ttl time.Duration) error {
	e.Chat, e.Expires = chatID, expires(e.At, ttl)
	return d.db.Update(func(tx kv.Tx) error { return d.msgVectors.store.Instantiate(tx).Set(K(chatID, msgID), &e) })
}

// MsgVector returns a message's vector, or ErrNotFound.
func (d *DB) MsgVector(chatID, msgID int64) (*Embedding, error) {
	var out *Embedding
	err := d.db.View(func(tx kv.Tx) error {
		var err error
		out, err = d.msgVectors.store.Instantiate(tx).Get(K(chatID, msgID))
		return err
	})
	return out, err
}

// SearchMessages returns up to k messages of the chat whose vectors are
// nearest to q, best first. The search is approximate: it reads the nearest
// partitions of the index.
func (d *DB) SearchMessages(chatID int64, q []float32, k int) ([]Hit, error) {
	var out []Hit
	err := d.db.View(func(tx kv.Tx) error {
		out = nil
		res, err := d.msgVectors.vectors.Instantiate(tx).Search(q).Where(embChat.Eq(chatID)).Limit(k).Run()
		if err != nil {
			return err
		}
		defer res.Close()
		for ; res.Valid(); res.Next() {
			key, err := res.Key()
			if err != nil {
				return err
			}
			out = append(out, Hit{ID: key.Second, Score: 1 - res.Distance()})
		}
		return nil
	})
	return out, err
}

// MsgIndexStats is what the message vector index has: its partitions and the
// vectors the upkeep has not placed yet.
func (d *DB) MsgIndexStats() (vector.Stats, error) {
	var out vector.Stats
	err := d.db.View(func(tx kv.Tx) (err error) {
		out, err = d.msgVectors.vectors.Instantiate(tx).Stats()
		return err
	})
	return out, err
}

// UnembeddedArchive returns the ids of archived messages of the chat that have
// no vector yet, at most n (for the startup backfill).
func (d *DB) UnembeddedArchive(chatID int64, n int) ([]int64, error) {
	var out []int64
	err := d.db.View(func(tx kv.Tx) error {
		vecs := d.msgVectors.store.Instantiate(tx)
		it, err := d.archive.Instantiate(tx).ReverseIterator(chatRange(chatID))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid() && len(out) < n; it.Next() {
			k, err := it.Key()
			if err != nil {
				return err
			}
			if ok, err := vecs.Has(k); err != nil {
				return err
			} else if !ok {
				out = append(out, k.Second)
			}
		}
		return nil
	})
	return out, err
}

// entityRange is every key (chatID, (entity, *)).
func entityRange(chatID, entity int64) kvutil.Range[Key3] {
	return kvutil.Between(K3(chatID, entity, math.MinInt64), K3(chatID, entity, math.MaxInt64))
}

// PutItemVector stores a memory item's vector; DeleteItemVector removes it.
func (d *DB) PutItemVector(chatID, entity int64, itemID uint64, e Embedding) error {
	e.Chat, e.Expires = chatID, 0
	return d.db.Update(func(tx kv.Tx) error {
		return d.itemVectors.store.Instantiate(tx).Set(K3(chatID, entity, int64(itemID)), &e)
	})
}

// DeleteItemVector removes a memory item's vector (missing is fine).
func (d *DB) DeleteItemVector(chatID, entity int64, itemID uint64) error {
	return d.db.Update(func(tx kv.Tx) error {
		return d.itemVectors.store.Instantiate(tx).Delete(K3(chatID, entity, int64(itemID)))
	})
}

// ItemVectors returns the vectors of an entity's items (item id → vector).
func (d *DB) ItemVectors(chatID, entity int64) (map[uint64]Embedding, error) {
	out := map[uint64]Embedding{}
	err := d.db.View(func(tx kv.Tx) error {
		clear(out)
		it, err := d.itemVectors.store.Instantiate(tx).Iterator(entityRange(chatID, entity))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid(); it.Next() {
			v, err := it.Value()
			if err != nil {
				return err
			}
			k, err := it.Key()
			if err != nil {
				return err
			}
			out[uint64(k.Second.Second)] = *v
		}
		return nil
	})
	return out, err
}

// ScoreItems returns the cosine similarity of q to every embedded item of an
// entity (item id → score); the items without a vector are missing.
func (d *DB) ScoreItems(chatID, entity int64, q []float32) (map[uint64]float32, error) {
	out := map[uint64]float32{}
	err := d.db.View(func(tx kv.Tx) error {
		clear(out)
		src, err := d.itemVectors.store.Records(tx, entityRange(chatID, entity))
		if err != nil {
			return err
		}
		res, err := d.itemVectors.vectors.Instantiate(tx).Search(q).Among(src).Run()
		if err != nil {
			return err
		}
		defer res.Close()
		for ; res.Valid(); res.Next() {
			k, err := res.Key()
			if err != nil {
				return err
			}
			out[uint64(k.Second.Second)] = 1 - res.Distance()
		}
		return nil
	})
	return out, err
}

// DeleteEntityVectors removes every item vector of an entity.
func (d *DB) DeleteEntityVectors(chatID, entity int64) error {
	return d.db.Update(func(tx kv.Tx) error {
		s := d.itemVectors.store.Instantiate(tx)
		it, err := s.Iterator(entityRange(chatID, entity))
		if err != nil {
			return err
		}
		var keys []Key3
		for ; it.Valid(); it.Next() {
			k, err := it.Key()
			if err != nil {
				it.Close()
				return err
			}
			keys = append(keys, k)
		}
		it.Close()
		for _, k := range keys {
			if err := s.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// PutMediaVector stores the vector of a cached sticker/GIF description for ttl.
func (d *DB) PutMediaVector(uniqueID string, e Embedding, ttl time.Duration) error {
	e.Chat, e.Expires = 0, expires(e.At, ttl)
	return d.db.Update(func(tx kv.Tx) error { return d.mediaVectors.store.Instantiate(tx).Set(mediaKey(uniqueID), &e) })
}

// NearestMedia returns up to k cached media whose description vectors are
// nearest to q, best first (Hit.ID is the media key hash).
func (d *DB) NearestMedia(q []float32, k int) ([]Hit, error) {
	var out []Hit
	err := d.db.View(func(tx kv.Tx) error {
		out = nil
		res, err := d.mediaVectors.vectors.Instantiate(tx).Nearest(q, k)
		if err != nil {
			return err
		}
		defer res.Close()
		for ; res.Valid(); res.Next() {
			key, err := res.Key()
			if err != nil {
				return err
			}
			out = append(out, Hit{ID: key.Second, Score: 1 - res.Distance()})
		}
		return nil
	})
	return out, err
}

// MediaByHash returns the cached media whose key hash is h, or ErrNotFound.
func (d *DB) MediaByHash(h int64) (*MediaDesc, error) {
	var out *MediaDesc
	err := d.db.View(func(tx kv.Tx) error {
		var err error
		out, err = d.media.Instantiate(tx).Get(K(0, h))
		return err
	})
	return out, err
}

// MediaHash is the key hash of a file_unique_id.
func MediaHash(uniqueID string) int64 { return mediaKey(uniqueID).Second }

// PurgeArchive deletes a member's archived messages and their vectors through
// the by-user index and returns the message ids removed.
func (d *DB) PurgeArchive(chatID, userID int64) ([]int64, error) {
	var ids []int64
	err := d.db.Update(func(tx kv.Tx) error {
		ids = nil
		idx := d.archiveByUser.Instantiate(tx)
		it, err := idx.Iterator(kvutil.Between(K3(chatID, userID, math.MinInt64), K3(chatID, userID, math.MaxInt64)))
		if err != nil {
			return err
		}
		for ; it.Valid(); it.Next() {
			k, err := it.Key()
			if err != nil {
				it.Close()
				return err
			}
			ids = append(ids, k.Second.Second)
		}
		it.Close()
		arch, vecs := d.archive.Instantiate(tx), d.msgVectors.store.Instantiate(tx)
		for _, id := range ids {
			for _, err := range []error{arch.Delete(K(chatID, id)), vecs.Delete(K(chatID, id)), idx.Delete(K3(chatID, userID, id))} {
				if err != nil && !errors.Is(err, kv.ErrNotFound) {
					return err
				}
			}
		}
		return nil
	})
	return ids, err
}

// GetEmbeddingMeta returns the stored embedding model (zero when none).
func (d *DB) GetEmbeddingMeta() (EmbeddingMeta, error) {
	var m EmbeddingMeta
	err := d.db.View(func(tx kv.Tx) error {
		v, err := d.meta.Instantiate(tx).Get("embedding")
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		m = *v
		return nil
	})
	return m, err
}

// wipeChangedEmbeddings deletes everything under the prefixes of the stores
// of embeddings when the model or the dimensions changed since the vectors
// were stored. It reports whether want is to be recorded (after the migration,
// which creates the store of the meta on a new database). A configuration
// without a model leaves everything as it is.
func (d *DB) wipeChangedEmbeddings(ctx context.Context, want EmbeddingMeta, prefixes ...[]byte) (bool, error) {
	if want.Model == "" {
		return false, nil
	}
	have, err := d.GetEmbeddingMeta()
	if err != nil {
		return false, err
	}
	if have == want {
		return false, nil
	}
	// A store of the records, its indexes and what it records of itself are
	// all under its prefix, so the migration finds a new, empty store.
	if have.Model != "" && (have.Model != want.Model || have.Dims != 0) {
		slog.Info("embedding model changed: wiping vectors", "old", have.Model, "new", want.Model, "dims", want.Dims)
		for _, p := range prefixes {
			if err := kv.DeleteRange(ctx, d.db, p, kv.PrefixEnd(p)); err != nil {
				return false, fmt.Errorf("wipe vectors: %w", err)
			}
		}
	}
	return true, nil
}

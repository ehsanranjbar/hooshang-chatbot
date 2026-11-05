package store

import (
	"errors"
	"math"
	"time"

	"github.com/ehsanranjbar/kvutil"
	"github.com/ehsanranjbar/kvutil/codec"
	"github.com/ehsanranjbar/kvutil/kv"
	"github.com/ehsanranjbar/kvutil/store/typed"

	"github.com/ehsanranjbar/hooshang/internal/rag"
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

// EmbeddingMeta records which model made the stored vectors; a change wipes
// them.
type EmbeddingMeta struct {
	Model string
	Dims  int
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
func (d *DB) PutMsgVector(chatID, msgID int64, v rag.Vector, ttl time.Duration) error {
	return d.db.Update(func(tx kv.Tx) error { return setTTL(d.msgVectors.Instantiate(tx), K(chatID, msgID), &v, ttl) })
}

// MsgVectors returns every message vector of the chat (id → vector): the
// in-memory index is loaded from it.
func (d *DB) MsgVectors(chatID int64) (map[int64]rag.Vector, error) {
	out := map[int64]rag.Vector{}
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.msgVectors.Instantiate(tx).Iterator(chatRange(chatID))
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
			out[k.Second] = *v
		}
		return nil
	})
	return out, err
}

// UnembeddedArchive returns the ids of archived messages of the chat that have
// no vector yet, at most n (for the startup backfill).
func (d *DB) UnembeddedArchive(chatID int64, n int) ([]int64, error) {
	var out []int64
	err := d.db.View(func(tx kv.Tx) error {
		vecs := d.msgVectors.Instantiate(tx)
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

// PutItemVector stores a memory item's vector; DeleteItemVector removes it.
func (d *DB) PutItemVector(chatID, entity int64, itemID uint64, v rag.Vector) error {
	return d.db.Update(func(tx kv.Tx) error { return d.itemVectors.Instantiate(tx).Set(K3(chatID, entity, int64(itemID)), &v) })
}

// DeleteItemVector removes a memory item's vector (missing is fine).
func (d *DB) DeleteItemVector(chatID, entity int64, itemID uint64) error {
	return d.db.Update(func(tx kv.Tx) error {
		err := d.itemVectors.Instantiate(tx).Delete(K3(chatID, entity, int64(itemID)))
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		}
		return err
	})
}

// ItemVectors returns the vectors of an entity's items (item id → vector).
func (d *DB) ItemVectors(chatID, entity int64) (map[uint64]rag.Vector, error) {
	out := map[uint64]rag.Vector{}
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.itemVectors.Instantiate(tx).Iterator(kvutil.Between(K3(chatID, entity, math.MinInt64), K3(chatID, entity, math.MaxInt64)))
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

// DeleteEntityVectors removes every item vector of an entity.
func (d *DB) DeleteEntityVectors(chatID, entity int64) error {
	return d.db.Update(func(tx kv.Tx) error {
		s := d.itemVectors.Instantiate(tx)
		it, err := s.Iterator(kvutil.Between(K3(chatID, entity, math.MinInt64), K3(chatID, entity, math.MaxInt64)))
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
func (d *DB) PutMediaVector(uniqueID string, v rag.Vector, ttl time.Duration) error {
	return d.db.Update(func(tx kv.Tx) error { return setTTL(d.mediaVectors.Instantiate(tx), mediaKey(uniqueID), &v, ttl) })
}

// MediaVectors returns every media vector by media key hash.
func (d *DB) MediaVectors() (map[int64]rag.Vector, error) {
	out := map[int64]rag.Vector{}
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.mediaVectors.Instantiate(tx).Iterator(chatRange(0))
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
			out[k.Second] = *v
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
		arch, vecs := d.archive.Instantiate(tx), d.msgVectors.Instantiate(tx)
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

// SetEmbeddingMeta records the embedding model in use.
func (d *DB) SetEmbeddingMeta(m EmbeddingMeta) error {
	return d.db.Update(func(tx kv.Tx) error { return d.meta.Instantiate(tx).Set("embedding", &m) })
}

// WipeVectors drops every stored vector (the embedding model changed).
func (d *DB) WipeVectors() error {
	return d.db.Update(func(tx kv.Tx) error {
		if err := deleteAll(d.msgVectors.Instantiate(tx), kvutil.Between(K(math.MinInt64, math.MinInt64), K(math.MaxInt64, math.MaxInt64))); err != nil {
			return err
		}
		if err := deleteAll(d.mediaVectors.Instantiate(tx), kvutil.Between(K(math.MinInt64, math.MinInt64), K(math.MaxInt64, math.MaxInt64))); err != nil {
			return err
		}
		return deleteAll(d.itemVectors.Instantiate(tx), kvutil.Between(K3(math.MinInt64, math.MinInt64, math.MinInt64), K3(math.MaxInt64, math.MaxInt64, math.MaxInt64)))
	})
}

// deleteAll deletes every key of a store in a range.
func deleteAll[K any, V any](s interface {
	Iterator(kvutil.Range[K]) (*typed.Iterator[K, V], error)
	Delete(K) error
}, r kvutil.Range[K]) error {
	it, err := s.Iterator(r)
	if err != nil {
		return err
	}
	var keys []K
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
}

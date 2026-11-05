package store

import (
	"errors"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/ehsanranjbar/kvutil/kv"
)

// Term is one entry of a group's glossary: a local word or phrase (اصطلاح) and
// what it means, as a member explained it. The glossary is capped; when it is
// full the least recently used term is dropped (LRU).
type Term struct {
	Term    string // as the member wrote it
	Norm    string // NormalizeTerm(Term); the key is derived from it
	Meaning string
	ByID    int64  // who explained it
	ByName  string // their display name then
	AddedAt time.Time
	UsedAt  time.Time // last explained or shown to the model
}

// NormalizeTerm folds a term for matching: lowercase, ي→ی, ك→ک, ة→ه, no
// tatweel, no diacritics, ZWNJ as a space, and runs of spaces collapsed.
func NormalizeTerm(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r == 'ي':
			r = 'ی'
		case r == 'ك':
			r = 'ک'
		case r == 'ة':
			r = 'ه'
		case r == 'ـ' || (r >= 'ً' && r <= 'ْ') || r == 'ٰ':
			continue
		case r == '‌' || r == '‏' || r == '‎' || r == ' ' || r == '\t' || r == '\n':
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// termKey is the key of a normalized term: its 64-bit FNV hash.
func termKey(chatID int64, norm string) Key {
	h := fnv.New64a()
	_, _ = h.Write([]byte(norm))
	return K(chatID, int64(h.Sum64()))
}

// Terms returns the chat's glossary, most recently used first.
func (d *DB) Terms(chatID int64) ([]Term, error) {
	var out []Term
	err := d.db.View(func(tx kv.Tx) error {
		var err error
		out, err = d.allTerms(tx, chatID)
		return err
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].UsedAt.After(out[j].UsedAt) })
	return out, err
}

func (d *DB) allTerms(tx kv.Tx, chatID int64) ([]Term, error) {
	it, err := d.terms.Instantiate(tx).Iterator(chatRange(chatID))
	if err != nil {
		return nil, err
	}
	defer it.Close()
	var out []Term
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// PutTerm adds the term or replaces its meaning, and marks it used. A new term
// in a full glossary (max entries) evicts the least recently used one, which
// is returned. One transaction.
func (d *DB) PutTerm(chatID int64, t Term, max int) (evicted *Term, err error) {
	t.Norm = NormalizeTerm(t.Term)
	if t.Norm == "" {
		return nil, errors.New("empty term")
	}
	now := time.Now()
	err = d.db.Update(func(tx kv.Tx) error {
		evicted = nil
		s := d.terms.Instantiate(tx)
		key := termKey(chatID, t.Norm)
		old, err := s.Get(key)
		switch {
		case err == nil:
			t.AddedAt = old.AddedAt
		case errors.Is(err, kv.ErrNotFound):
			t.AddedAt = now
			all, err := d.allTerms(tx, chatID)
			if err != nil {
				return err
			}
			for len(all) >= max && len(all) > 0 {
				lru := 0
				for i := range all {
					if all[i].UsedAt.Before(all[lru].UsedAt) {
						lru = i
					}
				}
				e := all[lru]
				if err := s.Delete(termKey(chatID, e.Norm)); err != nil {
					return err
				}
				evicted = &e
				all = append(all[:lru], all[lru+1:]...)
			}
		default:
			return err
		}
		t.UsedAt = now
		return s.Set(key, &t)
	})
	return evicted, err
}

// GetTerm returns the term, or ErrNotFound.
func (d *DB) GetTerm(chatID int64, term string) (*Term, error) {
	var out *Term
	err := d.db.View(func(tx kv.Tx) error {
		var err error
		out, err = d.terms.Instantiate(tx).Get(termKey(chatID, NormalizeTerm(term)))
		return err
	})
	return out, err
}

// DeleteTerm deletes the term; a missing term is not an error.
func (d *DB) DeleteTerm(chatID int64, term string) error {
	return d.db.Update(func(tx kv.Tx) error {
		return d.terms.Instantiate(tx).Delete(termKey(chatID, NormalizeTerm(term)))
	})
}

// TouchTerms marks the terms (by Norm) as used now, so they stay in the glossary.
func (d *DB) TouchTerms(chatID int64, norms []string) error {
	if len(norms) == 0 {
		return nil
	}
	now := time.Now()
	return d.db.Update(func(tx kv.Tx) error {
		s := d.terms.Instantiate(tx)
		for _, n := range norms {
			t, err := s.Get(termKey(chatID, n))
			if errors.Is(err, kv.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			t.UsedAt = now
			if err := s.Set(termKey(chatID, n), t); err != nil {
				return err
			}
		}
		return nil
	})
}

package store

import (
	"errors"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/ehsanranjbar/kvutil/kv"

	"github.com/ehsanranjbar/hooshang/internal/text"
)

// MediaDesc is the model's description of a sticker or GIF, shared by every
// chat and keyed by Telegram's file_unique_id (the same file has the same id
// everywhere and over time). It also keeps what is needed to send it again.
type MediaDesc struct {
	Text     string
	At       time.Time // when it was described
	UniqueID string
	Kind     string // "sticker" or "gif"
	FileID   string // the file to send (a bot's file ids stay valid); "" if never seen since
	Emoji    string // the sticker's emoji
	Uses     int    // how many times members sent it
	LastSeen time.Time
}

// mediaKey is the key of a file_unique_id: (0, its FNV-64a hash).
func mediaKey(uniqueID string) Key {
	h := fnv.New64a()
	h.Write([]byte("media:" + uniqueID))
	return K(0, int64(h.Sum64()))
}

// GetMediaDesc returns the cached description, or ErrNotFound.
func (d *DB) GetMediaDesc(uniqueID string) (*MediaDesc, error) {
	var out *MediaDesc
	err := d.db.View(func(tx kv.Tx) error {
		var err error
		out, err = d.media.Instantiate(tx).Get(mediaKey(uniqueID))
		return err
	})
	return out, err
}

// PutMediaDesc caches a description for ttl (memkv keeps it forever).
func (d *DB) PutMediaDesc(v MediaDesc, ttl time.Duration) error {
	if v.At.IsZero() {
		v.At = time.Now()
	}
	return d.db.Update(func(tx kv.Tx) error {
		return setMedia(d.media.Instantiate(tx), &v, ttl)
	})
}

// SeenMedia records that a member sent a cached sticker or GIF again: its
// latest file id, one more use, and a fresh ttl. A missing entry is a no-op.
func (d *DB) SeenMedia(uniqueID, fileID, emoji string, ttl time.Duration) error {
	return d.db.Update(func(tx kv.Tx) error {
		s := d.media.Instantiate(tx)
		v, err := s.Get(mediaKey(uniqueID))
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		v.UniqueID, v.FileID, v.Uses, v.LastSeen = uniqueID, fileID, v.Uses+1, time.Now()
		if emoji != "" {
			v.Emoji = emoji
		}
		return setMedia(s, v, ttl)
	})
}

func setMedia(s interface {
	Set(Key, *MediaDesc) error
	SetWithTTL(Key, *MediaDesc, time.Duration) error
}, v *MediaDesc, ttl time.Duration) error {
	err := s.SetWithTTL(mediaKey(v.UniqueID), v, ttl)
	if errors.Is(err, kv.ErrUnsupported) {
		err = s.Set(mediaKey(v.UniqueID), v)
	}
	return err
}

// SearchMedia returns up to n sendable stickers/GIFs (kind "" = both) that
// match the query: an emoji matches a sticker's emoji or the description,
// words match the description. Better matches first, then the most used.
// An empty query returns the most used.
func (d *DB) SearchMedia(kind, query string, n int) ([]MediaDesc, error) {
	terms := strings.Fields(text.Fold(query))
	type hit struct {
		m     MediaDesc
		score int
	}
	var hits []hit
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.media.Instantiate(tx).Iterator(chatRange(0))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid(); it.Next() {
			v, err := it.Value()
			if err != nil {
				return err
			}
			if v.FileID == "" || (kind != "" && v.Kind != kind) {
				continue
			}
			text := text.Fold(v.Text)
			emoji := strings.ReplaceAll(v.Emoji, "️", "")
			score := 0
			for _, t := range terms {
				t = strings.ReplaceAll(t, "️", "")
				switch {
				case emoji != "" && t == emoji:
					score += 3
				case strings.Contains(text, t):
					score++
				}
			}
			if len(terms) > 0 && score == 0 {
				continue
			}
			hits = append(hits, hit{*v, score})
		}
		return nil
	})
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].m.Uses > hits[j].m.Uses
	})
	var out []MediaDesc
	for i := 0; i < len(hits) && i < n; i++ {
		out = append(out, hits[i].m)
	}
	return out, err
}

// AllMedia returns every cached sticker and GIF description (the shared media
// cache), including the ones that cannot be sent. Used by the data export.
func (d *DB) AllMedia() ([]MediaDesc, error) {
	var out []MediaDesc
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.media.Instantiate(tx).Iterator(chatRange(0))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid(); it.Next() {
			v, err := it.Value()
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return nil
	})
	return out, err
}

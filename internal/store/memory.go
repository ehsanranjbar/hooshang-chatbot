package store

import (
	"errors"
	"sort"
	"time"

	"github.com/ehsanranjbar/kvutil/kv"
)

// GroupEntity is the entity id of the group's own memory in a chat.
const GroupEntity int64 = 0

// Item kinds. A kind says what an item is and how long it lives (see
// memory.Policy): members have facts, interests, quirks, relationships,
// boundaries, the admin's orders, dated episodes, stances, running jokes and
// looks (from profile photos); the group has events, terms and style samples;
// the bot itself has stances, jokes and promises.
const (
	KindFact         = "fact"
	KindInterest     = "interest"
	KindQuirk        = "quirk"
	KindRelationship = "relationship"
	KindBoundary     = "boundary"
	KindOrder        = "order"
	KindEpisode      = "episode"
	KindStance       = "stance"
	KindJoke         = "joke"
	KindLook         = "look"
	KindEvent        = "event"
	KindTerm         = "term"
	KindPromise      = "promise"
	KindSample       = "sample"
)

// Item is one remembered thing, of any entity.
type Item struct {
	ID      uint64
	Kind    string
	Text    string
	Note    string    // term: the meaning; look: which photo; event: unused
	By      int64     // who taught or said it (a term's teacher may forget it)
	Locked  bool      // written by the admin: the extractor keeps it as is
	Seen    int       // how many times it was observed (2+ = durable)
	Score   int       // event: the engagement of the messages it rests on
	FirstAt time.Time // when it was first recorded
	LastAt  time.Time // when it was last observed, used or confirmed
	Expires time.Time // zero = durable; else dropped after this
	Source  []int64   // message ids it rests on (at most 5)
}

// Memory is everything remembered about one entity of a chat: a member
// (entity = user id), the group (GroupEntity) or the bot itself (its id).
type Memory struct {
	Nickname       string
	NicknameLocked bool
	Summary        string
	SummaryLocked  bool
	Items          []Item
	Suppressed     []string // removed by the admin: the extractor must not add these again
	NextID         uint64   // the next item id (UpdateMemory assigns ids to items with ID 0)
	UpdatedAt      time.Time
}

// Kind returns the items of a kind, in stored order.
func (m *Memory) Kind(kind string) []Item {
	var out []Item
	for _, it := range m.Items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

// Find returns the item with the id, or nil.
func (m *Memory) Find(id uint64) *Item {
	for i := range m.Items {
		if m.Items[i].ID == id {
			return &m.Items[i]
		}
	}
	return nil
}

// Add appends an item and gives it an id. It returns the stored item.
func (m *Memory) Add(it Item) *Item {
	if m.NextID == 0 {
		m.NextID = 1
	}
	it.ID = m.NextID
	m.NextID++
	m.Items = append(m.Items, it)
	return &m.Items[len(m.Items)-1]
}

// Remove deletes the item with the id; a missing id is a no-op.
func (m *Memory) Remove(id uint64) {
	for i := range m.Items {
		if m.Items[i].ID == id {
			m.Items = append(m.Items[:i], m.Items[i+1:]...)
			return
		}
	}
}

// Prune drops expired items and gives ids to new ones.
func (m *Memory) Prune(now time.Time) {
	kept := m.Items[:0]
	for _, it := range m.Items {
		if !it.Expires.IsZero() && !it.Expires.After(now) {
			continue
		}
		if it.ID == 0 {
			if m.NextID == 0 {
				m.NextID = 1
			}
			it.ID = m.NextID
			m.NextID++
		}
		kept = append(kept, it)
	}
	m.Items = kept
}

// Empty reports whether nothing is remembered.
func (m *Memory) Empty() bool {
	return m.Nickname == "" && m.Summary == "" && len(m.Items) == 0
}

// SortItems orders items for display and capping: locked first, then the most
// seen, then the most recent.
func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Locked != b.Locked {
			return a.Locked
		}
		if a.Seen != b.Seen {
			return a.Seen > b.Seen
		}
		return a.LastAt.After(b.LastAt)
	})
}

// GetMemory returns the memory of an entity (zero when there is none), without
// its expired items.
func (d *DB) GetMemory(chatID, entity int64) (Memory, error) {
	var m Memory
	err := d.db.View(func(tx kv.Tx) error {
		v, err := d.memories.Instantiate(tx).Get(K(chatID, entity))
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		m = *v
		return nil
	})
	m.Prune(time.Now())
	return m, err
}

// UpdateMemory reads the memory (zero if missing), prunes it, lets fn change
// it, gives new items ids and writes it, in one transaction. fn may run more
// than once; it must not have side effects. It returns the result.
func (d *DB) UpdateMemory(chatID, entity int64, fn func(*Memory)) (Memory, error) {
	var out Memory
	err := d.db.Update(func(tx kv.Tx) error {
		s := d.memories.Instantiate(tx)
		m, err := s.Get(K(chatID, entity))
		if errors.Is(err, kv.ErrNotFound) {
			m, err = &Memory{}, nil
		}
		if err != nil {
			return err
		}
		now := time.Now()
		m.Prune(now)
		fn(m)
		m.Prune(now)
		if m.UpdatedAt.IsZero() {
			m.UpdatedAt = now
		}
		out = *m
		return s.Set(K(chatID, entity), m)
	})
	return out, err
}

// DeleteMemory forgets everything about an entity (/forgetme, /ignoreme).
func (d *DB) DeleteMemory(chatID, entity int64) error {
	return d.db.Update(func(tx kv.Tx) error {
		err := d.memories.Instantiate(tx).Delete(K(chatID, entity))
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		}
		return err
	})
}

// MemoriesOf returns every memory of the chat by entity id (one range scan),
// without expired items.
func (d *DB) MemoriesOf(chatID int64) (map[int64]Memory, error) {
	out := map[int64]Memory{}
	now := time.Now()
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.memories.Instantiate(tx).Iterator(chatRange(chatID))
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
			v.Prune(now)
			out[k.Second] = *v
		}
		return nil
	})
	return out, err
}

// CountMemories returns how many entities of the chat have a memory with at
// least one item (for /status).
func (d *DB) CountMemories(chatID int64) (int, error) {
	ms, err := d.MemoriesOf(chatID)
	n := 0
	for _, m := range ms {
		if !m.Empty() {
			n++
		}
	}
	return n, err
}

// Style is what a member's messages look like, counted as they arrive: no
// model involved. Hours are in Asia/Tehran.
type Style struct {
	Msgs    int            // messages counted
	Words   int            // words in them
	Emoji   int            // messages with an emoji
	Laughs  map[string]int // laugh forms seen ("خخخ", "😂", "هاها", …)
	Hours   [24]int        // messages per hour of the day
	ReplyTo map[int64]int  // who they reply to most (user id → count, at most 10 kept)
}

// Package store is the database of hooshang: chats, members with their memory profiles, recent
// messages and the audit log of memory edits. It is built on kvutil over Badger.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/ehsanranjbar/kvutil"
	"github.com/ehsanranjbar/kvutil/codec"
	"github.com/ehsanranjbar/kvutil/field"
	"github.com/ehsanranjbar/kvutil/index"
	"github.com/ehsanranjbar/kvutil/keyspace"
	"github.com/ehsanranjbar/kvutil/kv"
	"github.com/ehsanranjbar/kvutil/kv/badgerkv"
	"github.com/ehsanranjbar/kvutil/store/record"
	"github.com/ehsanranjbar/kvutil/store/typed"
)

// ErrNotFound is returned when a chat or member does not exist.
var ErrNotFound = kv.ErrNotFound

// Key is a key of two int64 components: (chatID, userID) for members, (chatID, messageID) for
// messages, (chatID, unixNano) for edits.
type Key = codec.Pair[int64, int64]

// K builds a Key.
func K(chatID, second int64) Key { return codec.NewPair(chatID, second) }

// Chat is the state of one group.
type Chat struct {
	Title          string
	Muted          bool
	LastBotAt      time.Time // when the bot last sent a message here
	MsgsSinceBot   int       // messages from members since the bot last spoke
	LastReactionAt time.Time // when the bot last reacted on its own

	MsgsSinceGroupUpdate int       // messages since the group memory was last updated
	GroupUpdatedAt       time.Time // when the group memory was last updated
}

// Item is one remembered thing.
type Item struct {
	Text   string
	Locked bool // written by the admin; the updater must keep it unchanged
}

// Profile is what the bot remembers about a member, in one chat.
type Profile struct {
	Nickname       string
	NicknameLocked bool // set by the admin; the updater must keep it
	Summary        string
	SummaryLocked  bool // set by the admin; the updater must keep it
	Facts          []Item
	Interests      []Item
	Quirks         []Item
	Relationships  []Item
	Boundaries     []Item
	Orders         []Item   // the admin's standing orders on how to treat this member; only the admin changes them
	Suppressed     []string // removed by the admin: the updater must not add these again
}

// Member is a member of a chat. Its id, (chatID, userID), is its key.
//
// The id is ALSO stored in the value on purpose. Do not add `msgpack:"-"`: kvutil hands the
// extensions (the by_username index) the old record without its id set (ext.Instance.Set reads it
// with the typed Get), so an index that reads the id would leave stale entries behind.
type Member struct {
	ID               Key
	FirstName        string
	LastName         string
	Username         string
	Profile          Profile
	NoTease          bool
	MsgsSinceUpdate  int
	LastSeen         time.Time
	ProfileUpdatedAt time.Time
}

func (m *Member) GetId() Key   { return m.ID }
func (m *Member) SetId(id Key) { m.ID = id }

// ChatID and UserID read the parts of the id.
func (m *Member) ChatID() int64 { return m.ID.First }
func (m *Member) UserID() int64 { return m.ID.Second }

// Message is a message of a chat, from a member or from the bot.
type Message struct {
	UserID        int64
	Name          string
	Username      string
	Text          string
	ReplyToID     int64
	ReplyToUserID int64
	Date          time.Time
	FromBot       bool
	Reactions     int    // current number of reactions on it
	Replies       int    // replies to it
	Described     bool   // its media description was added to Text
	BotReaction   string // the emoji the bot put on it ("" for none)
	ID            int64  `msgpack:"-"` // the message id (its key); set on read
}

// Engagement is how much a message caught the group's attention.
func (m *Message) Engagement() int { return 2*m.Reactions + m.Replies }

// GroupEvent is one remembered topic or event of a group. It expires a TTL
// after LastAt, so a topic that keeps coming back stays remembered.
type GroupEvent struct {
	Text    string
	Score   int // accumulated engagement of the messages it is based on
	FirstAt time.Time
	LastAt  time.Time
	ID      int64 `msgpack:"-"` // its key: FirstAt in unix nanoseconds; set on read
}

// Edit is one memory edit made by the admin.
type Edit struct {
	AdminID int64
	UserID  int64 // the member whose memory was edited
	Request string
	Ops     string // JSON of the operations, as the LLM gave them
	Before  Profile
	After   Profile
}

// DB is the database. All methods are safe for concurrent use.
type DB struct {
	db        kv.DB
	closer    func() error
	retention time.Duration

	chats      *typed.Store[int64, Chat]
	members    *record.Store[Key, Member]
	byUsername *index.FieldIndex[Key, Member, codec.Pair[int64, string]] // (chatID, lowercase username) -> member
	messages   *typed.Store[Key, Message]
	edits      *typed.Store[Key, Edit]
	events     *typed.Store[Key, GroupEvent]
	terms      *typed.Store[Key, Term]
	media      *typed.Store[Key, MediaDesc]
}

// Open opens the Badger database in dir. Messages expire after retention.
func Open(dir string, retention time.Duration) (*DB, error) {
	opt := badger.DefaultOptions(dir).WithLoggingLevel(badger.WARNING)
	base, err := badgerkv.Open(opt)
	if err != nil {
		return nil, fmt.Errorf("open badger: %w", err)
	}
	d, err := define(base, retention)
	if err != nil {
		return nil, errors.Join(err, base.Close())
	}
	d.closer = base.Close
	return d, nil
}

// OpenWith defines the stores on any kv.DB; tests use memkv.New().
func OpenWith(db kv.DB, retention time.Duration) (*DB, error) {
	return define(db, retention)
}

func define(db kv.DB, retention time.Duration) (*DB, error) {
	ks, err := keyspace.Open(db)
	if err != nil {
		return nil, fmt.Errorf("open keyspace: %w", err)
	}
	d := &DB{db: db, retention: retention}

	d.chats = typed.New[int64, Chat](ks.Prefix("chats"))
	d.members = record.New[Key, Member](ks.Prefix("members"))
	d.byUsername = index.On(d.members, "by_username",
		field.Computed[Member, codec.Pair[int64, string]]("ChatUsername",
			func(m *Member) ([]codec.Pair[int64, string], error) {
				if m.Username == "" {
					return nil, nil // no username: no entry in the index
				}
				return []codec.Pair[int64, string]{codec.NewPair(m.ID.First, strings.ToLower(m.Username))}, nil
			}))
	d.messages = typed.New[Key, Message](ks.Prefix("messages"))
	d.edits = typed.New[Key, Edit](ks.Prefix("memory_edits"))
	d.events = typed.New[Key, GroupEvent](ks.Prefix("group_events"))
	d.terms = typed.New[Key, Term](ks.Prefix("terms"))
	d.media = typed.New[Key, MediaDesc](ks.Prefix("media"))

	report, err := ks.Migrate(context.Background(), d.chats, d.members, d.messages, d.edits, d.events, d.terms, d.media)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if s := report.String(); s != "" {
		slog.Info("database migrated", "report", s)
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error {
	if d.closer != nil {
		return d.closer()
	}
	return nil
}

// ---------------------------------------------------------------- chats

// GetChat returns the chat, or a zero Chat if it is not stored yet.
func (d *DB) GetChat(chatID int64) (Chat, error) {
	var c Chat
	err := d.db.View(func(tx kv.Tx) error {
		v, err := d.chats.Instantiate(tx).Get(chatID)
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		c = *v
		return nil
	})
	return c, err
}

// UpdateChat reads the chat (zero if missing), lets fn change it and writes it, in one transaction.
// fn may run more than once if the transaction conflicts, so it must not have side effects.
func (d *DB) UpdateChat(chatID int64, fn func(*Chat)) error {
	return d.db.Update(func(tx kv.Tx) error {
		s := d.chats.Instantiate(tx)
		c, err := s.Get(chatID)
		if errors.Is(err, kv.ErrNotFound) {
			c, err = &Chat{}, nil
		}
		if err != nil {
			return err
		}
		fn(c)
		return s.Set(chatID, c)
	})
}

// ---------------------------------------------------------------- members

// GetMember returns the member, or ErrNotFound.
func (d *DB) GetMember(chatID, userID int64) (*Member, error) {
	var m *Member
	err := d.db.View(func(tx kv.Tx) error {
		var err error
		m, err = d.members.Instantiate(tx).Get(K(chatID, userID))
		return err
	})
	return m, err
}

// UpdateMember reads the member (a new one if missing), lets fn change it and writes it, in one
// transaction. fn may run more than once; it must not have side effects. It returns the result.
func (d *DB) UpdateMember(chatID, userID int64, fn func(*Member)) (*Member, error) {
	var out *Member
	err := d.db.Update(func(tx kv.Tx) error {
		s := d.members.Instantiate(tx)
		m, err := s.Get(K(chatID, userID))
		if errors.Is(err, kv.ErrNotFound) {
			m, err = &Member{ID: K(chatID, userID)}, nil
		}
		if err != nil {
			return err
		}
		fn(m)
		m.ID = K(chatID, userID) // fn must not move the member
		out = m
		return s.Set(m)
	})
	return out, err
}

// DeleteMember deletes the member (used by /forgetme). Deleting a missing member is not an error.
func (d *DB) DeleteMember(chatID, userID int64) error {
	return d.db.Update(func(tx kv.Tx) error {
		return d.members.Instantiate(tx).Delete(K(chatID, userID))
	})
}

// MemberByUsername finds a member of the chat by username (with or without @, any case), or
// returns ErrNotFound.
func (d *DB) MemberByUsername(chatID int64, username string) (*Member, error) {
	username = strings.TrimPrefix(username, "@")
	var m *Member
	err := d.db.View(func(tx kv.Tx) error {
		key, found, err := d.byUsername.Instantiate(tx).Find(codec.NewPair(chatID, strings.ToLower(username)))
		if err != nil {
			return err
		}
		if !found {
			return kv.ErrNotFound
		}
		m, err = d.members.Instantiate(tx).Get(key)
		return err
	})
	return m, err
}

// Members returns all members of the chat, in user id order.
func (d *DB) Members(chatID int64) ([]*Member, error) {
	var out []*Member
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.members.Instantiate(tx).Iterator(chatRange(chatID))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid(); it.Next() {
			v, err := it.Value()
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------- messages

// AddMessage stores a message. It expires after the retention on Badger; engines without TTL
// (memkv in tests) keep it.
func (d *DB) AddMessage(chatID, messageID int64, m *Message) error {
	return d.db.Update(func(tx kv.Tx) error {
		s := d.messages.Instantiate(tx)
		err := s.SetWithTTL(K(chatID, messageID), m, d.retention)
		if errors.Is(err, kv.ErrUnsupported) {
			err = s.Set(K(chatID, messageID), m)
		}
		return err
	})
}

// GetMessage returns one message, or ErrNotFound (also once it has expired).
func (d *DB) GetMessage(chatID, messageID int64) (*Message, error) {
	var m *Message
	err := d.db.View(func(tx kv.Tx) error {
		var err error
		m, err = d.messages.Instantiate(tx).Get(K(chatID, messageID))
		if m != nil {
			m.ID = messageID
		}
		return err
	})
	return m, err
}

// RecentMessages returns up to n of the latest messages of the chat, oldest first.
func (d *DB) RecentMessages(chatID int64, n int) ([]Message, error) {
	var out []Message
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.messages.Instantiate(tx).ReverseIterator(chatRange(chatID))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid() && len(out) < n; it.Next() {
			v, err := it.Value()
			if err != nil {
				return err
			}
			k, err := it.Key()
			if err != nil {
				return err
			}
			v.ID = k.Second
			out = append(out, *v)
		}
		return nil
	})
	// reverse to oldest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

// UpdateMessage lets fn change a stored message, keeping its original expiry.
// A missing or expired message is not an error; fn is then not called.
func (d *DB) UpdateMessage(chatID, messageID int64, fn func(*Message)) error {
	return d.db.Update(func(tx kv.Tx) error {
		s := d.messages.Instantiate(tx)
		m, err := s.Get(K(chatID, messageID))
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		fn(m)
		left := d.retention - time.Since(m.Date)
		if left <= 0 {
			return nil
		}
		err = s.SetWithTTL(K(chatID, messageID), m, left)
		if errors.Is(err, kv.ErrUnsupported) {
			err = s.Set(K(chatID, messageID), m)
		}
		return err
	})
}

// ---------------------------------------------------------------- group memory

// GroupEvents returns the group's events that are younger than ttl since they
// were last seen (Badger drops them itself; this also covers memkv), oldest first.
func (d *DB) GroupEvents(chatID int64, ttl time.Duration) ([]GroupEvent, error) {
	var out []GroupEvent
	cutoff := time.Now().Add(-ttl)
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.events.Instantiate(tx).Iterator(chatRange(chatID))
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
			if v.LastAt.Before(cutoff) {
				continue
			}
			v.ID = k.Second
			out = append(out, *v)
		}
		return nil
	})
	return out, err
}

// ReplaceGroupEvents writes the group's events: each one in events is stored
// (a zero ID gets one from FirstAt) and expires ttl after its LastAt; every
// stored event of the chat that is not in events is deleted. One transaction.
func (d *DB) ReplaceGroupEvents(chatID int64, events []GroupEvent, ttl time.Duration) error {
	return d.db.Update(func(tx kv.Tx) error {
		s := d.events.Instantiate(tx)
		keep := map[int64]bool{}
		for i := range events {
			e := events[i]
			if e.ID == 0 {
				e.ID = e.FirstAt.UnixNano()
			}
			for keep[e.ID] { // two new events in the same nanosecond
				e.ID++
			}
			keep[e.ID] = true
			left := ttl - time.Since(e.LastAt)
			if left <= 0 {
				continue
			}
			err := s.SetWithTTL(K(chatID, e.ID), &e, left)
			if errors.Is(err, kv.ErrUnsupported) {
				err = s.Set(K(chatID, e.ID), &e)
			}
			if err != nil {
				return err
			}
		}
		it, err := s.Iterator(chatRange(chatID))
		if err != nil {
			return err
		}
		var stale []int64
		for ; it.Valid(); it.Next() {
			k, err := it.Key()
			if err != nil {
				it.Close()
				return err
			}
			if !keep[k.Second] {
				stale = append(stale, k.Second)
			}
		}
		it.Close()
		for _, id := range stale {
			if err := s.Delete(K(chatID, id)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------- edits

// AddEdit appends an edit to the audit log of the chat.
func (d *DB) AddEdit(chatID int64, e *Edit) error {
	return d.db.Update(func(tx kv.Tx) error {
		return d.edits.Instantiate(tx).Set(K(chatID, time.Now().UnixNano()), e)
	})
}

// RecentEdits returns up to n of the latest edits of the chat, newest first.
func (d *DB) RecentEdits(chatID int64, n int) ([]Edit, error) {
	var out []Edit
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.edits.Instantiate(tx).ReverseIterator(chatRange(chatID))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid() && len(out) < n; it.Next() {
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

// ---------------------------------------------------------------- helpers

// chatRange is every key (chatID, *).
func chatRange(chatID int64) kvutil.Range[Key] {
	return kvutil.Between(K(chatID, math.MinInt64), K(chatID, math.MaxInt64))
}

// ChatIDs returns the ids of every known chat.
func (d *DB) ChatIDs() ([]int64, error) {
	var out []int64
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.chats.Instantiate(tx).Iterator(kvutil.Between[int64](math.MinInt64, math.MaxInt64))
		if err != nil {
			return err
		}
		defer it.Close()
		for ; it.Valid(); it.Next() {
			k, err := it.Key()
			if err != nil {
				return err
			}
			out = append(out, k)
		}
		return nil
	})
	return out, err
}

// Package store is the database of hooshang: chats, members, the memories of members, the
// group and the bot itself, recent messages, the media cache and the audit logs. It is built on
// kvutil over Badger.
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

	"github.com/ehsanranjbar/hooshang/internal/rag"
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
	MsgsSinceSelfUpdate  int       // the bot's own messages since its self memory was last updated

	LastGreetingAt  time.Time   // when the bot last answered a صبح/شب بخیر here
	LastBotQuestion BotQuestion // the question the bot last asked someone, so their next message counts as an answer

	// ModActions is the set of moderation actions the admin allows here
	// (action -> allowed). nil means "use the config default"; otherwise it is
	// the explicit allowed set (an action missing from it is not allowed).
	ModActions map[string]bool
}

// BotQuestion is a question the bot asked a member (its reply ended with «؟»).
type BotQuestion struct {
	UserID int64
	MsgID  int64
	At     time.Time
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
	NoTease          bool
	Ignored          bool // /ignoreme: the bot does not read, store or talk about this member
	Premium          bool
	LanguageCode     string
	MsgsSinceUpdate  int // the member's messages since their memory was last updated
	LastSeen         time.Time
	ProfileUpdatedAt time.Time // when their memory was last updated by the extractor
	LastGreetedAt    time.Time // when the bot last answered this member's صبح/شب بخیر
	SeededAt         time.Time // when their Telegram profile (photos, bio) was last read
	Style            Style     // what their messages look like (counted, not extracted)
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

// Edit is one memory edit made by the admin.
type Edit struct {
	AdminID int64
	UserID  int64 // the member whose memory was edited
	Request string
	Ops     string // JSON of the operations, as the LLM gave them
	Before  Memory
	After   Memory
}

// ModEntry is one moderation action of the admin, kept as an audit log.
type ModEntry struct {
	AdminID    int64
	Action     string // delete, mute, kick, ban, unban, pin, unpin
	TargetID   int64  // the member acted on (0 for pin/unpin of a message)
	TargetName string
	MsgIDs     []int64   // the messages deleted, pinned or unpinned
	Until      time.Time // when a mute ends (zero for other actions)
	Reason     string
	Err        string // why it failed or was refused ("" = it worked)
	At         time.Time
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
	memories   *typed.Store[Key, Memory]
	edits      *typed.Store[Key, Edit]
	media      *typed.Store[Key, MediaDesc]
	modLog     *typed.Store[Key, ModEntry]

	// Retrieval (RAG): archived messages and their vectors, item and media vectors.
	archive       *typed.Store[Key, Archived]
	archiveByUser *typed.Store[Key3, struct{}]
	msgVectors    *typed.Store[Key, rag.Vector]
	itemVectors   *typed.Store[Key3, rag.Vector]
	mediaVectors  *typed.Store[Key, rag.Vector]
	meta          *typed.Store[string, EmbeddingMeta]
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
	d.memories = typed.New[Key, Memory](ks.Prefix("memories"))
	d.edits = typed.New[Key, Edit](ks.Prefix("memory_edits"))
	d.media = typed.New[Key, MediaDesc](ks.Prefix("media"))
	d.modLog = typed.New[Key, ModEntry](ks.Prefix("mod_log"))
	d.archive = typed.New[Key, Archived](ks.Prefix("archive"))
	d.archiveByUser = typed.New[Key3, struct{}](ks.Prefix("archive_by_user"))
	d.msgVectors = typed.New[Key, rag.Vector](ks.Prefix("msg_vectors"))
	d.itemVectors = typed.New[Key3, rag.Vector](ks.Prefix("item_vectors"))
	d.mediaVectors = typed.New[Key, rag.Vector](ks.Prefix("media_vectors"))
	d.meta = typed.New[string, EmbeddingMeta](ks.Prefix("meta"))

	report, err := ks.Migrate(context.Background(), d.chats, d.members, d.messages, d.memories, d.edits, d.media, d.modLog,
		d.archive, d.archiveByUser, d.msgVectors, d.itemVectors, d.mediaVectors, d.meta)
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

// DeleteMessageIDs deletes the given stored messages of the chat (ids that are
// not stored are skipped). Used when the bot deleted them in Telegram.
func (d *DB) DeleteMessageIDs(chatID int64, ids []int64) error {
	return d.db.Update(func(tx kv.Tx) error {
		s := d.messages.Instantiate(tx)
		for _, id := range ids {
			if err := s.Delete(K(chatID, id)); err != nil && !errors.Is(err, kv.ErrNotFound) {
				return err
			}
		}
		return nil
	})
}

// DeleteMessagesBy deletes every stored message of the chat written by userID
// and returns how many there were. Used by /ignoreme.
func (d *DB) DeleteMessagesBy(chatID, userID int64) (int, error) {
	var n int
	err := d.db.Update(func(tx kv.Tx) error {
		s := d.messages.Instantiate(tx)
		it, err := s.Iterator(chatRange(chatID))
		if err != nil {
			return err
		}
		var keys []Key
		for ; it.Valid(); it.Next() {
			v, err := it.Value()
			if err != nil {
				it.Close()
				return err
			}
			if v.UserID != userID {
				continue
			}
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
		n = len(keys)
		return nil
	})
	return n, err
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

// ---------------------------------------------------------------- moderation log

// AddModEntry appends a moderation action to the audit log of the chat. A zero
// At is set to now.
func (d *DB) AddModEntry(chatID int64, e *ModEntry) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return d.db.Update(func(tx kv.Tx) error {
		return d.modLog.Instantiate(tx).Set(K(chatID, time.Now().UnixNano()), e)
	})
}

// RecentModEntries returns up to n of the latest moderation actions of the
// chat, newest first.
func (d *DB) RecentModEntries(chatID int64, n int) ([]ModEntry, error) {
	var out []ModEntry
	err := d.db.View(func(tx kv.Tx) error {
		it, err := d.modLog.Instantiate(tx).ReverseIterator(chatRange(chatID))
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

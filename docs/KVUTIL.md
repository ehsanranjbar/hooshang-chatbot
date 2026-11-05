# kvutil in hooshang

**Read this file instead of the kvutil source.** Do not open or explore the kvutil module. Everything
hooshang needs from kvutil is already wrapped in `internal/store`, which is written and tested
(`go test ./internal/store` passes on both memkv and Badger). The rest of the code uses
`internal/store` only and never imports `github.com/ehsanranjbar/kvutil/...` itself.

If a new storage need comes up, extend `internal/store` with the patterns in section 3, add a test
to `internal/store/store_test.go`, and run it on both engines.

## 1. The API of `internal/store` (use only this)

```go
db, err := store.Open(cfg.Storage.Path, cfg.Bot.MessageRetention) // Badger on disk
db, err := store.OpenWith(memkv.New(), time.Hour)                  // tests: in memory
defer db.Close()

// chats — a missing chat reads as a zero Chat (no error)
c, err := db.GetChat(chatID)                                  // (store.Chat, error)
err = db.UpdateChat(chatID, func(c *store.Chat) { c.Muted = true })

// members — missing member: errors.Is(err, store.ErrNotFound)
m, err := db.GetMember(chatID, userID)                        // (*store.Member, error)
m, err = db.UpdateMember(chatID, userID, func(m *store.Member) {  // creates when missing
    m.FirstName, m.Username = u.FirstName, u.Username
    m.MsgsSinceUpdate++
})
m, err = db.MemberByUsername(chatID, "@AliR")                  // "@" optional, any case
ms, err := db.Members(chatID)                                  // []*store.Member
err = db.DeleteMember(chatID, userID)                          // /forgetme

// messages — expire after retention on Badger
err = db.AddMessage(chatID, int64(msg.MessageID), &store.Message{...})
msgs, err := db.RecentMessages(chatID, 30)                     // []store.Message, oldest first

m, err := db.GetMessage(chatID, msgID)                          // *store.Message (ID set), or ErrNotFound
err = db.UpdateMessage(chatID, msgID, func(m *store.Message) { m.Reactions++ }) // keeps its expiry; missing = no-op

// group memory — each event expires ttl after its LastAt
evs, err := db.GroupEvents(chatID, ttl)                         // []store.GroupEvent (ID set)
err = db.ReplaceGroupEvents(chatID, evs, ttl)                   // writes evs, deletes the chat's others

// glossary (اصطلاحات) — LRU, capped per chat
ev, err := db.PutTerm(chatID, store.Term{Term: "چاییدن", Meaning: "سرما خوردن", ByID: ..., ByName: ...}, max) // ev = evicted term or nil
t, err := db.GetTerm(chatID, "چاییدن")                          // normalized lookup, or ErrNotFound
ts, err := db.Terms(chatID)                                     // most recently used first
err = db.TouchTerms(chatID, []string{t.Norm})                   // mark as used (keeps it from eviction)
err = db.DeleteTerm(chatID, "چاییدن")

// memory edit audit log
err = db.AddEdit(chatID, &store.Edit{AdminID: ..., UserID: ..., Request: ..., Ops: ..., Before: ..., After: ...})
es, err := db.RecentEdits(chatID, 10)                          // newest first
```

Types (in `internal/store/store.go`): `Chat`, `Member`, `Profile`, `Item`, `Message`, `Edit`,
`Key = codec.Pair[int64, int64]`. A member's IDs are `m.ChatID()` and `m.UserID()`.

**Rules:**

- The callbacks of `UpdateChat` / `UpdateMember` **may run more than once** (the transaction is
  retried on a conflict). Only change the struct inside them. Never send messages, call the LLM,
  append to outer slices or increment outer counters from inside the callback.
- Do slow work (LLM calls) **outside** a transaction: read → compute → `UpdateMember` to write.
  Inside the update, re-check whatever you depend on. For example the profile updater must keep
  `Locked` items and `Suppressed` from the *current* stored profile, not from the copy it read
  before the LLM call.
- `Profile` lives in `store` so that `memory` and `bot` can both use it without import cycles.
- Adding a field to a struct is safe: values are msgpack and old records decode with the zero
  value. **Renaming or removing a field, or changing its type, needs a store version bump and a migration**
  (section 3.5). Don't do it casually.
- Never add `msgpack:"-"` to `Member.ID` (see section 4).

## 2. Stores that exist

| Name (keyspace) | kvutil type | Key | Value | Notes |
|---|---|---|---|---|
| `chats` | `typed.Store` | `int64` chatID | `Chat` | |
| `members` | `record.Store` | `Pair(chatID, userID)` | `Member` | has index `by_username` |
| `members/by_username` | `index.FieldIndex` | `Pair(chatID, lower(username))` | → member key | members without a username are not indexed |
| `messages` | `typed.Store` | `Pair(chatID, messageID)` | `Message` | `SetWithTTL`; memkv has no TTL, so it falls back to `Set` |
| `memory_edits` | `typed.Store` | `Pair(chatID, unixNano)` | `Edit` | |
| `group_events` | `typed.Store` | `Pair(chatID, FirstAt unixNano)` | `GroupEvent` | `SetWithTTL` from `LastAt`; at most `group_memory.max_items` per chat |
| `media` | `typed.Store` | `Pair(0, FNV-64a of "media:"+file_unique_id)` | `MediaDesc` | sticker/GIF descriptions shared by all chats; `SetWithTTL` `media.cache_ttl` |
| `terms` | `typed.Store` | `Pair(chatID, FNV-64a of NormalizeTerm(term))` | `Term` | at most `glossary.max_items` per chat; `PutTerm` evicts the oldest `UsedAt` in the same transaction |

Group chat IDs are negative. Integer keys sort correctly with negative values (this is tested).

## 3. kvutil patterns (only if you must extend `internal/store`)

These are verified against the kvutil version pinned in `go.mod`.

### 3.1 Opening and defining

```go
base, err := badgerkv.Open(badger.DefaultOptions(dir).WithLoggingLevel(badger.WARNING))
ks, err := keyspace.Open(base)                  // short prefixes by name
s := typed.New[int64, Chat](ks.Prefix("chats")) // define every store before Migrate
report, err := ks.Migrate(ctx, s /*, every other store */) // once at startup; builds new indexes
```

Every store must be passed to `ks.Migrate`. A name the database has but the program doesn't claim
makes `Migrate` fail with `migrate.ErrUnknown`. To drop a store, call `ks.Retire("name")` before `Migrate`.

### 3.2 Transactions

```go
err := db.View(func(tx kv.Tx) error { v, err := s.Instantiate(tx).Get(k); ... })
err := db.Update(func(tx kv.Tx) error { return s.Instantiate(tx).Set(k, &v) })
```

A store is instantiated per transaction: `s.Instantiate(tx)`. Missing keys give `kv.ErrNotFound`.

### 3.3 Typed and record stores

```go
typed.New[K, V](prefix)        // Get(k) Set(k,&v) SetWithTTL(k,&v,ttl) Delete(k) Has(k)
                               // Iterator(r) ReverseIterator(r) All()
record.New[I, T](prefix)       // T has GetId() I / SetId(I) on *T;  Set(&v)  Get(id)  Delete(id)
```

Keys: integers, strings, `time.Time`, and `codec.Pair[A, B]` of these (nest a Pair as `B` for
three parts). Ranges: `kvutil.Between(lo, hi)`, `From`, `Until`, `Only`, `All`. A whole chat is
`kvutil.Between(K(chat, math.MinInt64), K(chat, math.MaxInt64))`.

Iterating (always close the iterator):

```go
it, err := s.Instantiate(tx).ReverseIterator(r)
if err != nil { return err }
defer it.Close()
for ; it.Valid(); it.Next() {
    v, err := it.Value()   // it.Key() for the key
    if err != nil { return err }
}
```

`SetWithTTL` on a record store that has indexes returns `ext.ErrTTLWithExtensions`. A store written with TTL must have no index.

### 3.4 An index on a record store

```go
idx := index.On(members, "by_username",
    field.Computed[Member, codec.Pair[int64, string]]("ChatUsername",
        func(m *Member) ([]codec.Pair[int64, string], error) {
            if m.Username == "" { return nil, nil }  // no entry
            return []codec.Pair[int64, string]{codec.NewPair(m.ID.First, strings.ToLower(m.Username))}, nil
        }))
key, found, err := idx.Instantiate(tx).Find(value) // first match
it, err := idx.Instantiate(tx).Match(value)        // all matches; it.Value() reads the record
```

Define the index before `ks.Migrate`; `Migrate` builds it for existing records. If what the
function computes changes, give the field a new version, or the old entries stay.

### 3.5 Changing a stored type

```go
type MemberV1 struct { /* the old shape, kept in the code */ }
members := record.New[Key, Member](ks.Prefix("members")).WithVersion(2).
    MigrateFrom(1, func(id Key, old *MemberV1) (*Member, error) { ...; return &m, nil })
```

## 4. A known kvutil bug (worked around)

`ext.Instance.Set` reads the **old** record for the extensions (indexes) with the plain typed
`Get`, which doesn't call `setID`. On a record store whose id is not serialized
(`ID ... msgpack:"-"`), an index whose field reads the id computes the old entry with a zero id, so
**stale index entries are left behind on update**. This was found by
`TestStore/.../old username still indexed`.

The workaround in hooshang: `Member.ID` *is* serialized in the value. Keep it that way until the
bug is fixed in kvutil (`store/ext/store.go`: `Set` and `Delete` both read `old` with `s.data.Get(key)`).

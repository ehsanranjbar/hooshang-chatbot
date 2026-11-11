# kvutil in hooshang

**Read this file instead of the kvutil source.** Do not open or explore the kvutil module. Everything
hooshang needs from kvutil is already wrapped in `internal/store`, which is written and tested
(`go test ./internal/store` passes on both memkv and Badger). The rest of the code uses
`internal/store` only and never imports `github.com/ehsanranjbar/kvutil/...` itself.

If a new storage need comes up, extend `internal/store` with the patterns in section 3, add a test
to `internal/store/store_test.go`, and run it on both engines.

## 1. The API of `internal/store` (use only this)

```go
db, err := store.Open(cfg.Storage.Path, store.Options{            // Badger on disk
    Retention: cfg.Bot.MessageRetention,
    EmbeddingModel: model, EmbeddingDims: cfg.LLM.EmbeddingDims,  // a change of either wipes the vectors
})
db, err := store.OpenWith(memkv.New(), store.Options{Retention: time.Hour, EmbeddingDims: 256}) // tests
db.StartUpkeep(ctx)                                             // background: vector partitions, expiry
err = db.Upkeep(ctx)                                            // tests/tools: do it all now
defer db.Close()                                                // stops the upkeep, then closes

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
n, err := db.DeleteMessagesBy(chatID, userID)                  // /ignoreme: the member's stored messages; n = how many
err = db.DeleteMessageIDs(chatID, []int64{12, 13})             // messages the bot deleted in Telegram

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

// moderation audit log (mod_log)
err = db.AddModEntry(chatID, &store.ModEntry{AdminID: ..., Action: "mute", TargetID: ..., Until: ..., Err: ""})
es, err := db.RecentModEntries(chatID, 10)                     // newest first

// the shared sticker/GIF cache, all of it (the data export)
all, err := db.AllMedia()                                       // []store.MediaDesc

// retrieval (rag.go): vectors are []float32 as the model gave them
err = db.Archive(chatID, msgID, store.Archived{...}, ttl)
err = db.PutMsgVector(chatID, msgID, store.Embedding{Vec: v, Hash: h, At: now}, ttl)
hits, err := db.SearchMessages(chatID, q, k)                    // []store.Hit{ID, Score=cosine}, best first; approximate
err = db.PutItemVector(chatID, entity, itemID, store.Embedding{...})
scores, err := db.ScoreItems(chatID, entity, q)                 // map[itemID]cosine, exact
err = db.PutMediaVector(uniqueID, store.Embedding{...}, ttl)
hits, err = db.NearestMedia(q, k)                                // Hit.ID = media key hash; db.MediaByHash(h)
st, err := db.MsgIndexStats()                                    // vector.Stats{Partitions, Unplaced}

// memory edit audit log
err = db.AddEdit(chatID, &store.Edit{AdminID: ..., UserID: ..., Request: ..., Ops: ..., Before: ..., After: ...})
es, err := db.RecentEdits(chatID, 10)                          // newest first
```

Types (in `internal/store/store.go`, `memory.go`, `rag.go`): `Chat`, `Member` (identity, counters,
`Style`), `Memory` and `Item` (what is remembered about an entity), `Message`, `Edit`, `ModEntry`,
`Archived`, `Key = codec.Pair[int64, int64]`, `Key3 = (int64, (int64, int64))`. A member's IDs are
`m.ChatID()` and `m.UserID()`.

**Rules:**

- The callbacks of `UpdateChat` / `UpdateMember` **may run more than once** (the transaction is
  retried on a conflict). Only change the struct inside them. Never send messages, call the LLM,
  append to outer slices or increment outer counters from inside the callback.
- Do slow work (LLM calls) **outside** a transaction: read → compute → `UpdateMember` to write.
  Inside the update, re-check whatever you depend on. For example the profile updater must keep
  `Locked` items and `Suppressed` from the *current* stored profile, not from the copy it read
  before the LLM call.
- `Memory` and `Item` live in `store` so that `memory` and `bot` can both use them without import cycles.
- 2026-11-05: `Profile`, `group_events` and `terms` were replaced by `memories` **without a
  migration** (the data directory is reset). `Member` lost `Profile`.
- Adding a field to a struct is safe: values are msgpack and old records decode with the zero
  value. **Renaming or removing a field, or changing its type, needs a store version bump and a migration**
  (section 3.5). Don't do it casually.
- Fields added so far, all zero for old records: `Chat.ModActions` (`nil` = use the config's
  `features.moderation.actions`, otherwise the explicit allowed set) and `Member.Ignored`
  (`/ignoreme`; a purged member is kept as `{ID, FirstName, Ignored}` so the flag survives).
- Never add `msgpack:"-"` to `Member.ID` (see section 4).

## 2. Stores that exist

| Name (keyspace) | kvutil type | Key | Value | Notes |
|---|---|---|---|---|
| `chats` | `typed.Store` | `int64` chatID | `Chat` | |
| `members` | `record.Store` | `Pair(chatID, userID)` | `Member` | has index `by_username` |
| `members/by_username` | `index.FieldIndex` | `Pair(chatID, lower(username))` | → member key | members without a username are not indexed |
| `messages` | `typed.Store` | `Pair(chatID, messageID)` | `Message` | `SetWithTTL`; memkv has no TTL, so it falls back to `Set` |
| `memory_edits` | `typed.Store` | `Pair(chatID, unixNano)` | `Edit` | |
| `memories` | `typed.Store` | `Pair(chatID, entity)` | `Memory` | entity = user id, `GroupEntity` (0) for the group, the bot's id for itself; items of every kind (facts, orders, episodes, events, terms, samples, …); `GetMemory`, `UpdateMemory`, `DeleteMemory`, `MemoriesOf`; expired items are pruned on read and write |
| `media` | `typed.Store` | `Pair(0, FNV-64a of "media:"+file_unique_id)` | `MediaDesc` | sticker/GIF descriptions shared by all chats; `SetWithTTL` `features.media.cache_ttl` |
| `mod_log` | `typed.Store` | `Pair(chatID, unixNano)` | `ModEntry` | audit log of the admin's moderation actions (also refused and failed ones); `AddModEntry`, `RecentModEntries(chat, n)` |
| `archive` | `typed.Store` | `Pair(chatID, messageID)` | `Archived` | member messages kept for retrieval, `SetWithTTL` `features.rag.archive_ttl` |
| `archive_by_user` | `typed.Store` | `(chatID, (userID, messageID))` | empty | lets `PurgeArchive` delete one member's rows; same TTL |
| `msg_embeddings` | `ext.Store` | `Pair(chatID, messageID)` | `Embedding` | vectors of archived messages; `by_vector` (cosine, partitioned + quantized; searched with `Where(Chat == chatID)`), `by_expiry` (`Expires` > 0) |
| `item_embeddings` | `ext.Store` | `(chatID, (entity, itemID))` | `Embedding` | vectors of memory items; `by_vector` exact, searched `Among` one entity's range; never expire |
| `media_embeddings` | `ext.Store` | `Pair(0, media hash)` | `Embedding` | vectors of sticker/GIF descriptions; `by_vector` partitioned + quantized; `by_expiry` |
| `meta` | `typed.Store` | `string` | `EmbeddingMeta` | `"embedding"`: the model and dims of the vectors; a change wipes the three stores above before `Migrate` |

A store with extensions refuses `SetWithTTL` (`ext.ErrTTLWithExtensions`), so an `Embedding` carries
`Expires` (unix seconds) and an upkeep job deletes the expired ones through `by_expiry`. One
`upkeep.Runner` per DB runs it every 10 minutes, and the partition upkeep of the vector indexes
(placing new vectors, splitting and dissolving partitions) every second.

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

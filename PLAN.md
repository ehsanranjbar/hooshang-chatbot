# Hooshang (هوشنگ) — implementation spec

A Telegram group bot that chats in Persian. It keeps a memory of each member **per group** (items with
evidence), of the group and of itself,
uses it to joke, tease and lightly flirt, reads Hafez faals and poems from Ganjoor, and lets the
admin change its memory by plain conversation.

This is a spec. Implement it as written. When something is not specified, choose the simplest
option and leave a `// NOTE:` comment.

---

## 0. Rules for the implementing agent

1. **Do not read the kvutil source.** Storage is done: `internal/store` is written and tested. Read
   `docs/KVUTIL.md` (sections 1–2) and use only the `store` API. Do not import kvutil packages
   outside `internal/store`.
2. **Do not change `internal/store`** unless a task below says so. If you must, add a test and keep
   `go test ./internal/store` green.
3. **Work through the tasks in section 9 in order.** Finish each task's "Done when" checks before
   starting the next. Do not start Telegram work before tasks T1–T4 pass their tests.
4. **Allowed dependencies only:** stdlib, `github.com/mymmrac/telego` (v1.12.x), `gopkg.in/yaml.v3`,
   `github.com/go-text/typesetting` + `github.com/go-text/render` (the faal image, §8a),
   plus what `internal/store` already uses. Write the LLM client and the Ganjoor client with `net/http`.
   No other modules.
5. Use the exact package names, file names and exported signatures in section 3. You may add
   unexported helpers freely.
6. Logging: `log/slog` only. No package-level mutable state; dependencies are passed into constructors.
7. Every package with logic has `_test.go` files. Tests must not need the network, Telegram or LM
   Studio: use `httptest.Server` and the fake LLM (`llm.Fake`). Integration checks against LM Studio
   go under the build tag `//go:build lmstudio`.
8. After every task, run: `gofmt -l .` (must print nothing), `go vet ./...` and `go test ./...`.

---

## 1. Decisions (fixed)

| Concern | Choice |
|---|---|
| Go | 1.27, module `github.com/ehsanranjbar/hooshang` |
| Telegram | `github.com/mymmrac/telego` v1.12, long polling |
| LLM | Own OpenAI-compatible client (`POST {base_url}/chat/completions`) |
| Model (dev) | LM Studio `http://localhost:1234/v1`, `google/gemma-4-26b-a4b`. One set of `llm.*` settings for whatever model is configured (no per-model config) |
| Model (later) | OpenRouter (`https://openrouter.ai/api/v1`), same client |
| Storage | kvutil + Badger via `internal/store` (done) |
| Poems | `https://api.ganjoor.net` |
| Memory scope | Per group (chatID, userID). No sharing between groups. |
| Teasing and flirting | On by default; members opt out with `/notease` |
| Admin | One hard admin = `telegram.owner_id`. Checked through the `auth.Admins` interface so more admins can be added later. |
| Memory editing | Admin speaks in natural language; LLM tools that only admin messages get (section 6) |

### 1.1 Verified facts about the model (LM Studio + Gemma 4, measured 2026-11-04)

- Gemma **reasons before answering** (`message.reasoning_content`, about 180 tokens even for a tiny task).
  Always send `max_tokens` ≥ **2048**. A smaller value gives `finish_reason: "length"` and an empty `content`.
  Ignore `reasoning_content`.
- **Tool calling works.** The response has `finish_reason: "tool_calls"` and
  `message.tool_calls[].function.{name, arguments}`, where `arguments` is a JSON *string*. It takes
  about 9s per round on this machine.
- **JSON output:** `response_format: {"type":"json_schema","json_schema":{"name":..., "schema":...}}`
  **without `"strict": true`** gives clean JSON. **`strict: true` produces degenerate garbage, so never send it.**
  Without `response_format` the model wraps JSON in ```` ```json ```` fences, so the parser must strip fences anyway.
- **The non-strict schema is only a hint and is not enforced.** Asked for `{"answer": ...}`, the
  model returned `{"2+2": 4}`. So every JSON prompt (`memory_extract.tmpl`) must **spell out the exact
  JSON shape with an example in the prompt text**, and the code must validate after parsing: unknown keys
  ignored, missing sections treated as empty, and if *no* known key is present, it counts as a parse
  failure (keep the old profile).
- Requests are slow (seconds) and the local server handles them roughly one at a time. Design for
  queues, not parallelism.

- **Benchmark (docs/MODELS.md):** Gemma 4 sometimes ignores `edit_memory` and writes its native
  tool-call syntax (`<|tool_call>call:…`) as text, which `stripToolMarkup` removes. It produces
  nothing usable with `json_schema`, so `llm.json_schema` stays false. It doesn't reliably obey
  "reply with `-`" after a tool, so after a faal the code drops the follow-up itself.

### 1.2 Verified Ganjoor API

- `GET https://api.ganjoor.net/api/ganjoor/hafez/faal` returns a random Hafez ghazal.
- `GET https://api.ganjoor.net/api/ganjoor/poem/random?poetId={id}` returns a random poem of a poet.
- Each `plainText` line is **one hemistich** (mesra). `verses[]` holds the same lines properly typeset
  (ZWNJs, punctuation), with `versePosition` 0/1 for the right/left hemistich. The client uses
  `verses[]` when every entry is 0/1, and `plainText` otherwise (bands, prose).
- Response fields used: `id` (number), `title`, `fullTitle`, `fullUrl` (path such as `/hafez/ghazal/sh298`),
  `plainText` (hemistichs separated by `\r\n`; can contain `U+00A0` (NBSP)), `poemSummary` (a string or null,
  prefixed with `هوش مصنوعی: `).
- Poet IDs: حافظ 2, خیام 3, فردوسی 4, مولوی/مولانا 5, نظامی 6, سعدی 7, پروین 8, عطار 9, وحشی 11, رودکی 12,
  باباطاهر 28, شهریار 35, ایرج 88.

---

## 2. Layout (exact)

```text
cmd/hooshang/main.go
config.example.yaml
internal/config/config.go, features.go, config_test.go, features_test.go
internal/llm/client.go, types.go, json.go, tools.go, fake.go, client_test.go, json_test.go, tools_test.go
internal/store/            DONE: do not change (docs/KVUTIL.md)
internal/auth/auth.go
internal/ratelimit/limiter.go, limiter_test.go
internal/ganjoor/client.go, format.go, poets.go, client_test.go, format_test.go
internal/memory/profile.go, ops.go, updater.go, profile_test.go, ops_test.go, updater_test.go
internal/prompts/prompts.go, *.tmpl (embedded), prompts_test.go
internal/i18n/fa.go
internal/transcribe/transcribe.go, transcribe_test.go   (whisper.cpp, §6e)
internal/bot/bot.go, access.go, trigger.go, worker.go, reply.go, tools.go, commands.go, faal.go,
             send.go, telegram.go, trigger_test.go, reply_test.go, commands_test.go
docs/KVUTIL.md            DONE
```

---

## 3. Package specs

### 3.1 `config`

```go
type Config struct {
    Telegram struct {
        Token   string `yaml:"token"`    // env HOOSHANG_TELEGRAM_TOKEN overrides
        OwnerID int64  `yaml:"owner_id"` // required, > 0
    } `yaml:"telegram"`
    LLM struct {
        BaseURL     string        `yaml:"base_url"`     // default http://localhost:1234/v1
        APIKey      string        `yaml:"api_key"`      // env HOOSHANG_LLM_API_KEY overrides; default "lm-studio"
        Model       string        `yaml:"model"`        // required
        MemoryModel string        `yaml:"memory_model"` // default = Model
        Temperature float64       `yaml:"temperature"`  // default 0.9 (memory calls always use 0.2)
        MaxTokens   int           `yaml:"max_tokens"`   // default 2048, min 1024
        Timeout     time.Duration `yaml:"timeout"`      // default 120s
        Tools       bool          `yaml:"tools"`        // default true
        JSONSchema  bool          `yaml:"json_schema"`  // default false: response_format for memory extraction
    } `yaml:"llm"`
    Bot struct {
        Name                string         `yaml:"name"`                 // default "هوشنگ"
        Aliases             []string       `yaml:"aliases"`              // default ["هوشی", "hooshang"]
        Language            string         `yaml:"language"`             // default "fa"
        HistoryMessages     int            `yaml:"history_messages"`     // default 30
        MessageRetention    time.Duration  `yaml:"message_retention"`    // default 72h
        SpontaneousChance   float64        `yaml:"spontaneous_chance"`   // default 0.07, range [0,1]
        SpontaneousCooldown time.Duration  `yaml:"spontaneous_cooldown"` // default 5m
        MinMsgsBetween      int            `yaml:"min_msgs_between"`     // default 8
        Modes               map[string]int `yaml:"modes"`                // default joke:3 tease:3 flirt:1 compliment:2 comment:2 sticker:1
        Persona             struct{ Text string; Notes []string }        // the identity paragraph and extra lines of the system prompt (§7)
        Human               struct{ Typing{Enabled, CharsPerSecond, Min, Max}; LateChance; LateMax; QuietHours; Moods []string; Bubbles int } // §7
        RateLimit struct {
            GlobalPerMinute int `yaml:"global_per_minute"` // default 10
            ChatPerMinute   int `yaml:"chat_per_minute"`   // default 6
        } `yaml:"rate_limit"`
    } `yaml:"bot"`
    Features Features `yaml:"features"` // the optional capabilities, below
    Nastaliq struct {
        Font   string `yaml:"font"`   // a Nastaliq .ttf; empty = embedded fonts
        Shaper string `yaml:"shaper"` // auto (default), harfbuzz, go
    } `yaml:"nastaliq"`                // shared by the faal and poem cards
    Storage struct {
        Path string `yaml:"path"` // default ./data
    } `yaml:"storage"`
}

func Load(path string) (*Config, error) // read YAML → defaults → strict decode → env overrides → Validate
func (c *Config) Validate() error       // token, owner_id, model required; ranges checked; mode keys ∈ {joke,tease,flirt,compliment,comment}
```

**Features.** Every optional capability lives under `features:`; the rest of the file (`telegram`,
`llm`, `bot`, `nastaliq`, `storage`) is always on. **A feature is on when its section is present**
in the file, and off when the section is absent or commented out. Inside a present section,
`enabled: false` also turns it off (the other keys are kept, and not validated). An empty section
(`roast:`) is on with all defaults.

```go
type Features struct {
    Profiles    *ProfilesFeature    `yaml:"profiles"`     // was memory
    GroupMemory *GroupMemoryFeature `yaml:"group_memory"`
    Glossary    *GlossaryFeature    `yaml:"glossary"`
    Media       *MediaFeature       `yaml:"media"`        // + send
    Faal        *FaalFeature        `yaml:"faal"`         // enabled, image
    Poem        *PoemFeature        `yaml:"poem"`         // enabled, image (was faal.poem_images)
    Roast       *RoastFeature       `yaml:"roast"`        // enabled
    Voice       *VoiceFeature       `yaml:"voice"`        // §6e
    Moderation  *ModerationFeature  `yaml:"moderation"`   // §6f
}
func (f *FaalFeature) On() bool { return f != nil && f.Enabled } // one per feature; safe on nil
func (f Features) Active() []string // the names that are on, for /status and -check
```

Nil pointer = absent section = off. Each feature struct has `UnmarshalYAML(*yaml.Node)` that first
fills the receiver with the feature's defaults, then decodes the node through a method-less alias
type, so keys left out keep their default and an explicit `0` stays `0`. `Load` decodes with
`KnownFields(true)` (a typo is an error; `UnmarshalYAML` checks the keys of its own section, which
yaml does not), and before that scans the top-level keys: the old `memory`, `group_memory`,
`glossary`, `media` and `faal` give one error that names the new path of each. Defaults and ranges:

| Feature | Keys (default) |
|---|---|
| `profiles` | `update_every` 15, `max_age` 24h, `context_messages` 150, `max_items` 10. Off: no updater jobs, no profiles or orders in prompts, no admin rules, no `get_memory` / `edit_memory`; `/me` and `/forgetme` say it is off |
| `group_memory` | `ttl` 168h, `max_items` 15, `update_every` 50, `in_prompt` 4, `prompt_chance` 0.3 |
| `glossary` | `max_items` 100, `in_prompt` 8. Off: no `/terms` (says it is off), no `learn_term` / `forget_term` |
| `media` | `photos`, `stickers`, `gifs`, `send` (all true), `words` 30, `cache_ttl` 2160h, `max_queue` 20, `frames` 3, `ffmpeg` "". `send` gates `find_media` / `send_media` apart from describing |
| `faal` | `image` true. Off: `/faal` says it is off, no `hafez_faal` tool (so the «فال» phrase does nothing) |
| `poem` | `image` true. Off: `/poem` says it is off, no `random_poem` |
| `roast` | none. Off: `/roast` says it is off |
| `moderation` | `actions` all of delete, mute, kick, ban, unban, pin, unpin; `default_mute` 1h, `max_mute` 168h, `max_delete` 20 (1..100). `actions` must be a subset of the seven, `default_mute` <= `max_mute`. Off: no `moderate` / `set_moderation` tools and no moderation block in the admin rules |
| `voice` | `whisper` "", `model` (required), `language` fa, `threads` 0, `max_duration` 120s, `max_queue` 10, `max_words` 120, `video_notes` true. The model file must exist; an `.en` model is refused for a language other than `en` |

`/status` lists the features that are on. `llm.tools` stays the global switch for tool calling.
`main` builds the Ganjoor client only when faal or poem is on, the card renderer only when an
`image` is on, the memory updater only when profiles or group_memory is on, and the transcriber
only when voice is on and whisper-cli, ffmpeg and the model are found (else a warning, and voices
stay `[ویس]`). `-check` loads and validates the config, prints the features that are on and
whether the tools they need (ffmpeg, whisper-cli, the model, a Nastaliq shaper) were found, never
prints the token or API key, and exits 1 on an invalid config or a voice feature that cannot run.

`Load` sets an empty `llm.memory_model` to `llm.model`. Memory calls use the same `max_tokens`,
`timeout` and `json_schema` as chat, at temperature 0.2.

`config.example.yaml` holds every key with its default and a comment, with every feature section
present. `main` takes the `-config` flag (default `config.yaml`) and `-check`.

### 3.2 `llm`

```go
type Message struct {
    Role       string     `json:"role"` // system | user | assistant | tool
    Content    string     `json:"content"`
    ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
    ToolCallID string     `json:"tool_call_id,omitempty"`
}
type ToolCall struct { ID, Name, Arguments string } // marshal to/from the wire shape {id, type:"function", function:{name, arguments}}
type Tool struct { Name, Description string; Parameters json.RawMessage } // JSON Schema object
type Request struct {
    Model       string
    Messages    []Message
    Tools       []Tool
    Temperature float64
    MaxTokens   int
    Schema      json.RawMessage // non-nil → response_format json_schema (never "strict")
    SchemaName  string
}
type Response struct { Content string; ToolCalls []ToolCall; FinishReason string }

type Client interface { Chat(ctx context.Context, req Request) (Response, error) }

func New(baseURL, apiKey string, timeout time.Duration) *HTTPClient // implements Client
```

- `HTTPClient.Chat`: one retry after 2s on a network error or HTTP 5xx/429; no retry on 4xx. An error
  includes the status and the first 300 bytes of the body. `Content` passes through `Clean`.
- `func Clean(s string) string` removes `<think>…</think>` blocks and trims space.
- `func ExtractJSON(s string) ([]byte, error)` strips ```` ``` ````/```` ```json ```` fences, then
  returns the outermost `{…}` that parses as JSON.
- `tools.go`:
  ```go
  type ToolFunc func(ctx context.Context, arguments string) (string, error) // result goes back to the model
  func RunTools(ctx context.Context, c Client, req Request, handlers map[string]ToolFunc, maxRounds int) (Response, error)
  ```
  The loop runs the call; while `ToolCalls` is non-empty and rounds < `maxRounds`, it appends the
  assistant message (with its tool calls), runs each handler, and appends `role:"tool"` messages
  (`ToolCallID`). An unknown tool or a handler error becomes the tool result `"error: …"`; it doesn't
  abort the loop. When rounds run out, it calls once more **without tools** to get a final text.
  `maxRounds` = 3.
- `fake.go`: `type Fake struct { Responses []Response; Requests []Request }`. It returns the
  responses in order and records the requests. Every other package's tests use it.

### 3.3 `auth`

```go
type Admins interface { IsAdmin(chatID, userID int64) bool }
type Owner struct{ ID int64 }                    // the only implementation for now
func (o Owner) IsAdmin(_, userID int64) bool { return userID == o.ID }
```

Admin checks always go through `Admins`. Nothing compares against `OwnerID` directly except
`Owner` itself and the access control in section 5, which needs the owner's ID to check group membership.

### 3.4 `ratelimit`

```go
func New(globalPerMinute, chatPerMinute int, now func() time.Time) *Limiter
func (l *Limiter) Reserve(chatID int64, n int) bool // atomically takes n slots in BOTH the global and the chat window, or none
func (l *Limiter) Free(chatID int64) (global, chat int)
```

Sliding window of 60s, using timestamps of reservations. It is mutex-protected and has no
goroutines. Tests use a fake `now`.

### 3.5 `ganjoor`

```go
type Poem struct { ID int64; Title, FullTitle, URL string; Hemistichs []string; Summary string }
func New(baseURL string, hc *http.Client) *Client // baseURL default https://api.ganjoor.net
func (c *Client) Faal(ctx context.Context) (*Poem, error)
func (c *Client) RandomPoem(ctx context.Context, poetID int) (*Poem, error)
func PoetID(name string) (int, bool)              // matches Persian names in §1.2, ignoring ZWNJ/spaces/ی-ي/ک-ك
func Format(p *Poem, maxCouplets int) string      // see below
```

- Parsing: split `plainText` on `\r\n` or `\n`, replace `U+00A0` (NBSP) with a space, trim, and drop empty
  lines. Strip the `هوش مصنوعی:` prefix from `Summary`. Build `URL` as `"https://ganjoor.net" + fullUrl`.
- `Format`: the first line is `📜 {FullTitle}`, then a blank line, then couplets as
  `hemistich1\nhemistich2` separated by a blank line. With `maxCouplets > 0`, the output is cut and `…` added.
- Results are cached for 30s: one cached faal and one per poetId.
- Tests use `httptest.Server` with the sample JSON saved under `internal/ganjoor/testdata/`
  (capture one faal and one random response from the live API once with curl).

### 3.6 `memory`

One model for everything remembered (`store.Memory`, keyed `(chatID, entity)`): a member (entity
= user id), the group (`store.GroupEntity` = 0) and the bot itself (its user id). A memory has a
nickname and summary (lockable), `Items` and `Suppressed` texts. An `Item` has an `ID`, a `Kind`,
`Text`, `Note` (a term's meaning), `By`, `Locked`, `Seen`, `Score` (events), `FirstAt`, `LastAt`,
`Expires` (zero = durable) and `Source` (up to 5 message ids).

Kinds and their `memory.Policies`: members → `fact`, `interest`, `quirk`, `relationship`,
`boundary` (durable, extractable), `episode` (extractable, 14 days), `stance`, `joke`
(extractable), `order` (admin only), `look` (from profile photos, 90 days); the group → `event`
(the group job, TTL `features.group_memory.ttl` from `LastAt`), `term` (anyone, LRU by `LastAt`),
`sample` (the group job); the bot → `stance`, `joke`, `promise` (7 days).

```go
func Merge(m *store.Memory, ext Extracted, kinds []string, now time.Time, maxItems int)
//   drops kinds the extractor may not write and suppressed texts; a stored item with the same kind and
//   folded text is re-observed only when the extraction cites a message it did not rest on (or cites
//   nothing and the item is older than a day): Seen++, LastAt, sources merged, expiry pushed back;
//   new items start at Seen 1 with the kind's TTL; items left out are kept; once-seen unlocked items
//   unseen for 30 days are dropped; each kind is capped at maxItems (locked, then most seen, then most recent).
func Apply(m *store.Memory, ops []Op, now time.Time) ([]string, error) // edit_memory: add (locked), remove (by item_id or text; suppresses), replace
func Render(m store.Memory) string          // nickname, summary, one line per kind in RenderKinds (episodes dated); no orders/terms/events
func RenderItems(m, items, now) string      // the same with a chosen subset (RAG)
func RenderOrders / RenderSelf / RenderEvents / RenderSamples / RenderStyle
func Samples(msgs, names, n) []string       // 3–15-word human messages, names → «فلانی», newest first
func NewUpdater(db, c, model, UpdaterConfig{ContextMessages, MaxItems, Language, MaxTokens, JSONSchema, BotID, Group, Embed}, logger)
func (u *Updater) Enqueue(chat, user) / EnqueueGroup(chat) / EnqueueSelf(chat) / Run(ctx)
func (u *Updater) UpdateNow(ctx, chat, user) / UpdateGroupNow(ctx, chat) / UpdateSelfNow(ctx, chat)
```

`UpdateNow` (a member): needs 3+ of their messages in the window; the prompt (`memory_extract.tmpl`)
shows the known items as `kind: text` and the transcript as `[id] name: text` with the target's
lines marked `>>`; the model returns `{"nickname","summary","items":[{"kind","text","msgs":[ids]}]}`
(`extractSchema` when `json_schema` is on); `Merge` runs inside `UpdateMemory` against the current
stored memory; then `Member.ProfileUpdatedAt`/`MsgsSinceUpdate` are reset and the items are handed to
`Embed`. A failed call or invalid JSON keeps the old memory.

`UpdateSelfNow` (the bot): every `features.profiles.self_every` (20) of its own messages
(`Chat.MsgsSinceSelfUpdate`), its lines and the lines they answered go to `self_extract.tmpl`, which
returns stances, jokes and promises; rendered into the tasks as "What you yourself have said here before".

`UpdateGroupNow`: see §6a; it also refreshes the `sample` items (`features.profiles.style_samples`).

**Style (`store.Style`, `bot.countStyle`)**: counted per member on every message, no model: messages,
words, messages with an emoji, laugh forms, messages per hour (Tehran), who they reply to (10 kept),
which stickers/GIFs they send (`Media`, 30 kept; behind `find_media({member})`).
`RenderStyle` makes one Persian line («پیام‌های کوتاه، پر از ایموجی، شب‌فعال، می‌خنده با خخخ، بیشتر
با علی حرف می‌زنه») after 20 messages; it is appended to the member's profile in prompts.

**Analysis (`memory/analysis.go`, `features.profiles.analysis`)**: the model's own read of a member,
written in Persian from everything known — the rendered memory, the style line, their three most-sent
stickers (descriptions) and up to 60 of their own messages (the archive when RAG is on, else the
recent messages) — plus the previous analysis to revise. Covers temperament, humour and how they take
jokes, what they are proud of or touchy about, their role and closeness to others, how they argue and
show affection, what lands and what to avoid, how to talk to them; grounded in the material, guesses
marked, no clinical labels or excluded topics, `max_words` (150). Stored on `Memory`
(`Analysis`, `AnalysisAt`, `AnalysisMsgs`) and run at the end of a member update once they have
`min_msgs` (30) messages and again after `every` (60) more. Rendered as «تحلیل: …» after the summary,
so it reaches the reply prompt (which says to let it shape tone and jokes but never quote it), `/me`
and `get_memory`; `/forgetme` deletes it with the memory.

**Telegram seed (`bot/seed.go`, `features.profiles.seed`)**: a member's first message (or one after
`refresh`) queues, on the describer's queue, a read of their profile: up to `photos` profile photos
described by the vision model ("profile picture of a group member") into `look` items (90 days), and
`getChat` for the bio («بیو: …») and birthday («تولد: ۱۴ آبان») as facts when Telegram exposes them.

### 3.7 `prompts`

All prompt text lives in `internal/prompts/*.tmpl` (`text/template`, embedded with `embed`).
`prompts.go` exposes typed render functions:

```go
func System(d SystemData) (string, error)         // persona + rules (§7) + language + time + bot name
func ReplyTask(d TaskData) (string, error)        // "reply to message X from Y"
func SpontaneousTask(d TaskData) (string, error)  // mode + target
func MemoryExtract(d ExtractData) (string, error)
func FaalTabir(d FaalData) (string, error)
func AdminRules(memory, moderation bool) string    // appended to system for admin messages (§6, §6f)
```

Templates are written in **English** and say "Always answer in {{.Language}} (Persian)." The model
follows English instructions more reliably. Each render function has a test that it renders
without error and contains its key fields.

### 3.7b `text`

`internal/text` is the one definition of "the same word": `Fold` (lowercase, ي/ك/ة→ی/ک/ه, no
tatweel or diacritics, ZWNJ and direction marks as spaces, whitespace collapsed), `Squash` (Fold
without spaces: names, poets, memory dedup) and `FoldKeepLen` (positions preserved, for whole-word
matching). `memory.Normalize`, `ganjoor.normalizePersian`, `store.NormalizeTerm`, `bot.normalizeName`
and `bot.toLowerFold` are thin wrappers over it.

### 3.8 `i18n`

`i18n/jalali.go`: `Jalali(t)` (jalaali-js civil algorithm), `FormatDate` «پنجشنبه ۱۴ آبان ۱۴۰۵»,
`FormatDateTime`, `When(t, now)` («امروز», «دیروز», else the date; used by the group memory), and the
greeting word lists `MorningGreetings` / `NightGreetings`.

`fa.go` has constants for every fixed string the bot sends: errors, confirmations, help, and so on,
for example `GanjoorDown = "گنجور الان در دسترس نیست، بعداً امتحان کن 🙏"`. No Persian string literals in
other packages except the templates.

### 3.9 `bot`

```go
type Deps struct {
    Cfg     *config.Config
    TG      *telego.Bot
    DB      *store.DB
    LLM     llm.Client
    Ganjoor *ganjoor.Client
    Limiter *ratelimit.Limiter
    Admins  auth.Admins
    Memory  *memory.Updater   // nil when neither profiles nor group_memory is on
    Transcriber transcribe.Transcriber // nil = no voice transcription (§6e)
    FaalImage, PoemImage *faalimg.Renderer // nil = text
}
func New(d Deps) (*Bot, error)          // calls GetMe once and keeps the bot's ID and username
func (b *Bot) Run(ctx context.Context) error
```

- **`telegram.go`**: a narrow interface over telego so tests can fake it:
  ```go
  type TG interface {
      SendMessage(ctx context.Context, p *telego.SendMessageParams) (*telego.Message, error)
      SendChatAction(ctx context.Context, p *telego.SendChatActionParams) error
      GetChatMember(ctx context.Context, p *telego.GetChatMemberParams) (telego.ChatMember, error)
      LeaveChat(ctx context.Context, p *telego.LeaveChatParams) error
  }
  ```
  `*telego.Bot` satisfies it, so `Deps.TG` can be typed as `TG` internally. Later additions (see
  `telegram.go`): the moderation calls of §6f (`BanChatMember`, `UnbanChatMember`,
  `RestrictChatMember`, `DeleteMessage`, `DeleteMessages`, `PinChatMessage`, `UnpinChatMessage`), and
  `GetUserProfilePhotos` and `GetChat`, which are wired but not used yet (a later phase).
- **Polling**: `UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{Timeout: 30, AllowedUpdates: []string{"message", "my_chat_member"}})`.
  Messages older than 2 minutes at handling time are ignored, which skips the backlog after downtime.
- **`send.go`**: `func (b *Bot) send(ctx, chatID int64, replyTo int, text string) error`, the **only**
  function that calls `SendMessage`. It:
  - splits into chunks of at most 4000 runes on line breaks;
  - sets `ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}` when `replyTo` is greater than 0;
  - stores each sent message with `FromBot: true` and `UpdateChat` (`LastBotAt = now`, `MsgsSinceBot = 0`);
  - on a 429, logs and gives up. It doesn't retry.

  **Callers must have reserved limiter slots first** (one per chunk; compute the chunks before reserving).
- **Message handling order** (`bot.go`):
  1. `my_chat_member` → access control (§5).
  2. `message`: ignore if `From == nil` or `From.IsBot`. Text = `messageText` (`media.go`):
     the text, or for media `[label: title] caption`, e.g. `[آهنگ: شجریان – بوی باران]`,
     `[فایل: جزوه.pdf] برای فردا`, `[نظرسنجی: شام چی بخوریم؟]`, `[استیکر: 😂]`. Titles are cut at 80
     runes. Join/leave service messages go to `handleMembership` (§7); other service messages are ignored.
  3. Access control (§5). Not allowed → stop.
  4. Store: `AddMessage`; `UpdateMember` (names and username, `LastSeen`, `MsgsSinceUpdate++`); `UpdateChat` (`MsgsSinceBot++`, `Title`).
  5. If `MsgsSinceUpdate >= UpdateEvery`, or (`ProfileUpdatedAt` is older than `MaxAge` and `MsgsSinceUpdate >= 3`) → `Memory.Enqueue`.
  6. A command (text starts with `/`; strip any `@botusername` suffix) → `commands.go`. Stop.
  7. `IsAddressed` or `isFollowUp` → enqueue a reply job. Otherwise, outside `bot.human.quiet_hours`:
     `maybeGreet` → a greeting job; else `ShouldReact` → a reaction (or a sticker job, 20%); else
     `ShouldSpontaneous` → a spontaneous job.
- **`trigger.go`** (pure functions, fully unit-tested):
  ```go
  func IsAddressed(m *telego.Message, botID int64, botUsername string, names []string, private bool) bool
  //   private chat; or reply to a message whose From.ID == botID; or an entity "mention" equal to @botUsername (case-insensitive);
  //   or a "text_mention" of botID; or a name/alias appearing as a whole word (Persian-aware: neighbors are not letters; ZWNJ counts as a letter boundary).
  func ShouldSpontaneous(c store.Chat, now time.Time, cfg SpontaneousConfig, roll float64) bool
  //   !Muted && now-LastBotAt >= Cooldown && MsgsSinceBot >= MinMsgsBetween && roll < Chance
  func PickMode(weights map[string]int, roll float64) string
  func PickTarget(recent []store.Message, members map[int64]*store.Member, botID int64, roll float64) (int64, bool)
  //   among distinct authors of the last 15 messages, excluding the bot and members with NoTease; ok=false if none
  ```
  For `mode ∈ {tease, flirt}`, the job needs a target; if `PickTarget` fails the mode falls back to `comment`.
- **`worker.go`**: one worker goroutine per chat, created on demand. The queue is a buffered channel
  with capacity 5. A reply job is dropped (with a log) when the queue is full; a spontaneous job is
  dropped if the worker is busy or the queue is non-empty. A worker exits after 10 minutes idle. Workers
  stop on ctx cancellation. While a job runs, it sends the `typing` chat action every 4s.
- **`reply.go`**: `func (b *Bot) runReply(ctx, job)`:
  1. **Reserve first**: a reply needs `Limiter.Reserve(chat, 1)`; a spontaneous message needs
     `Free().chat >= ChatPerMinute/2` and then `Reserve(chat, 1)`. On failure, drop the job silently
     (log at debug).
  2. Context: the last `history_messages` (15) messages **within `history_window` (20m)**, so old
     topics drop out, plus the replied-to message even when it's older. Lines are
     `[{id}] {name} (@{username}): {text}` with `↩︎[{id}]` for replies and `(you)` for the bot. The
     triggering message is marked `>>`, and the task says to answer it and not revisit older topics.
  3. Profiles are **sparse on purpose**, so replies stay about the conversation: the people the message
     is about (reply target, `@mentions`, the roast or spontaneous target) are always included; the
     author always, then transcript members and random members within `bot.profile_budget`. Joke,
     comment and compliment spontaneous messages get no profiles.
  4. Messages: `system` = `prompts.System` (+ `prompts.AdminRules()` if `Admins.IsAdmin`), then one
     `user` message that contains the profiles, the transcript and the task.
  5. Tools: `hafez_faal`, `random_poem`, plus `get_memory` and `edit_memory` **only when the author is admin**
     (§6). No tools when `cfg.LLM.Tools` is false.
  6. `llm.RunTools(…, maxRounds 3)`. Post-process: `Clean`, strip a leading `{botname}:` or
     `**{botname}**:`, trim. If the result is empty, send nothing.
  7. `deliver` (§7): mentions, bubbles, typing delay, one limiter slot per extra message; the first
     bubble replies to the triggering message (spontaneous messages are not replies).
- **`tools.go`**: tool definitions (JSON schemas) and handlers:
  - `hafez_faal({wish?: string})` runs the faal flow (§8). It returns to the model:
    `"sent faal: {FullTitle}. The poem, meaning and interpretation are already sent. Reply with a very short line or nothing."`
  - `random_poem({poet?: string})` runs the poem flow (§8) and returns a similar note.
  - `get_memory({member: string})` and `edit_memory({member: string, ops: Op[]})`: see §6.
- **`commands.go`**: see §4. Each command reply reserves 1 slot; if that fails, it does nothing.

---

## 4. Commands

| Command | Who | Behavior |
|---|---|---|
| `/start`, `/help` | everyone | `i18n.Help` |
| `/faal [نیت]` | everyone | Faal flow (§8). Reserves 2. `i18n.FeatureOff` when `features.faal` is off. |
| `/poem [شاعر]` | everyone | (`i18n.FeatureOff` when `features.poem` is off.) Poem flow (§8). Reserves 1, or 2 when commented. Unknown poet → `i18n.UnknownPoet` with the poet list. |
| `/roast` (reply or `@user`) | everyone | `i18n.FeatureOff` when `features.roast` is off. Else a reply job with the task "roast {target} with real bite, and jab the requester". Refused (`i18n.NoTeaseTarget`) if the target has `NoTease`. With no target → `i18n.RoastWho`. |
| `/terms` | everyone | The glossary (§6b), most recently used first, or `i18n.NoTerms`; `i18n.FeatureOff` when the glossary is off |
| `/me` | everyone | `memory.Render` of your own profile, or `i18n.NothingYet`; `i18n.FeatureOff` when profiles are off |
| `/notease`, `/teaseok` | everyone | Sets `NoTease`; confirms |
| `/forgetme` | everyone | `DeleteMember`; confirms (`i18n.FeatureOff` when profiles are off) |
| `/ignoreme`, `/unignoreme` | everyone | Privacy switch, see "Ignoring a member" below. Works even when profiles are off |
| `/mute`, `/unmute` | admin | `Chat.Muted` (spontaneous only); confirms |
| `/status` | admin | Model, limiter free slots, number of workers, updater queue length, member count, the features that are on, the voice queue, the moderation actions allowed here, last 3 edits |

Non-admins using admin commands: no reply.

### Ignoring a member (`/ignoreme`, `/unignoreme`)

A privacy switch for the member themselves, per group, independent of every feature.

- `/ignoreme` sets `Member.Ignored`. From then on `handleMessage`, right after the sender and
  access checks, drops every message of that member before anything else runs: nothing is stored,
  counted, described or transcribed, and there is no reply, reaction or spontaneous roll, even when
  the message names the bot. The only message that gets through the gate is `/unignoreme`
  (handled in the gate itself and not stored; a stale one from the backlog is not run).
- **Purge** at `/ignoreme`: the member's stored messages are deleted (`DeleteMessagesBy`), and the
  member record is reduced to `{ID, FirstName, Ignored: true}` (profile, orders, username, counters
  gone) so that the flag survives. A hook comment in `handleIgnoreMe` marks where the archive and
  vector purge go once those exist.
- **Excluded everywhere the bot could talk about them:** `renderProfiles`, `renderOrders`,
  `aboutIDs`, `namedIDs`, the random profiles, `PickTarget` / `pickTarget`, `/roast`
  (`i18n.IgnoredTarget`) and `resolveMember` (so `get_memory` / `edit_memory` cannot reach them).
  Moderation (§6f) still resolves them (`resolveModerationTarget`): being ignored shields nobody
  from the admin. Reactions are per message, not per user, and are left alone.
- `/unignoreme` clears the flag; what was purged stays purged.
- Limit: a quote of an ignored member inside someone else's message cannot be scrubbed.

---

## 5. Access control (`access.go`)

```go
func (b *Bot) allowed(ctx context.Context, chat telego.Chat, from *telego.User) bool
```

- **Private chat:** allowed only if `Admins.IsAdmin(chat.ID, from.ID)`. Otherwise log
  `"ignored private message" user_id=…` (this is how the owner finds their ID) and stop.
- **Group/supergroup:** allowed only if the **owner** is a member. The check calls
  `GetChatMember(chat, OwnerID)` and accepts `MemberIsMember()` or a status of `creator`,
  `administrator` or `member` (`restricted` only if `MemberIsMember()`). The result is cached per chat
  for 1h (allowed) or 5m (denied). On an API error, deny and don't leave; the next attempt checks again.
  If denied, send `i18n.NotAllowedHere` once (no limiter needed) and call `LeaveChat`.
- **`my_chat_member`:** when the bot's new status is `member` or `administrator` and `From.ID` is not
  an admin, leave immediately. When the admin added it, cache the chat as allowed.
- Channels and every other chat type: ignored.

---

## 6. Admin memory editing (natural language)

There are no editing commands. The admin says things like «هوشنگ، علی دیگه تهران نیست، رفته شیراز» or
«قضیهٔ آشپزی سارا رو فراموش کن».

- Only when `Admins.IsAdmin(chat, author)`, the reply job gets these tools and the `AdminRules()` text:
  - `get_memory({member})` returns `memory.Render` of the member, plus a numbered list of items per section.
  - `edit_memory({member, ops:[{action, section, text, old_text?}]})` applies `memory.Apply` inside
    `UpdateMember`, then `AddEdit(chat, {AdminID, UserID, Request: the admin's message text, Ops: raw arguments, Before, After})`.
    It returns the change list, or `"error: …"`.
- `member` is resolved by `resolveMember(chatID, s string, job)`, in this order: `"reply"` or empty →
  the replied-to user; `@username` → `MemberByUsername`; digits → user ID; otherwise a match on first
  name, last name or full name (normalized: ی/ي, ک/ك, ZWNJ, case) against `Members(chat)`.
  0 matches → `"error: no member matches …"`. More than 1 → `"error: ambiguous: name1 (@u1), name2 (@u2)"`.
  The model then asks the admin.
- The `AdminRules()` text tells the model: use `edit_memory` only for an explicit request to
  remember, forget or correct something; for an unclear request, ask instead; after editing, confirm in
  one short Persian line what changed.
- Non-admin messages **never** get these tools. Test this in `reply_test.go`: the fake LLM records
  the request, and the test asserts the tool names for an admin and a non-admin author.

---

### Admin orders

- When the admin gives an order in a strict tone, the bot obeys at once, in the same reply (an
  ordered apology is the apology itself), overriding the persona; only the hard limits stay.
- An order about a member («با سارا شوخی نکن», «علی رو آقای مهندس صدا کن») is saved with
  `edit_memory` in the member's `orders` section (`Profile.Orders`, locked; `Merge` always keeps
  the current orders and the updater never sees them).
- `renderOrders` puts the orders of **everyone in the conversation** (author, reply target,
  mentions, spontaneous target, every member who wrote in the transcript) at the top of reply and
  spontaneous prompts as "standing orders … you MUST follow them". Unlike profiles they are not
  subject to `profile_budget`.

## 6c. Media descriptions

Photos, stickers and GIFs are described by the chat model (a vision model; Gemma 4 is) so the
transcript shows them: `[استیکر: 😂 — گربه‌ای که از خنده غش کرده]`, `[عکس: ساحل در غروب] کپشن`.

- **What is sent:** a photo's smallest size with a side ≥ 512 px (or the largest); a static
  sticker itself (WEBP → PNG with `x/image/webp`); a TGS (Lottie) sticker's thumbnail. A video
  sticker (WEBM) or a GIF (MP4) is described from `features.media.frames` (3) frames: the clip itself is
  downloaded (≤ 20 MB, `maxClipBytes`) to a temp file, ffprobe reads its duration and one ffmpeg
  run (30 s timeout) writes PNG frames at the centres of `frames` equal slices of the clip
  (`select` filter by timestamp, scaled to ≤ 512 px on the longer side); they all go in one
  request (`llm.Message.Images` sends any number as OpenAI content parts) and the prompt says
  they are frames in order, to describe the motion. ffmpeg is optional (`features.media.ffmpeg`, else
  PATH, resolved once): when it is missing, the clip is too big, or extraction fails or gives
  fewer than 2 frames, the thumbnail (its first frame) is used as before, logged at Debug; a
  failure here never fails the description. `features.media.frames: 1` means thumbnail only. Files come
  from `GetFile` + `FileDownloadURL`; only the text is kept, never the image.
- **Prompt** (`media_describe.tmpl`): what is in it, any text in it, and for stickers/GIFs the
  reaction it expresses; never guess who a person is; at most `features.media.words` (30) words, Persian.
  `cleanDescription` makes it one line, caps it at words+10, and drops leaked reasoning.
- **Cache:** sticker and GIF descriptions are stored in the shared `media` store by
  `file_unique_id` (same file → same id in every chat) for `cache_ttl` (90 days), so a repeated
  sticker costs nothing. Photos aren't cached.
- **When:** after a media message is stored, a cached description is applied at once; otherwise a
  job goes on the describer's queue (one background goroutine, `max_queue` 20, extra jobs are
  skipped). A message the bot is about to answer is described first, synchronously
  (`describeTrigger` in `runReply`). `Message.Described` marks a done message.
- **Questions about a picture:** when the bot answers a message that replies to a member's photo,
  sticker or GIF (or carries one itself), `questionImage` downloads that picture again (for a
  video sticker or GIF, its frames) and attaches it to the reply call, with a note to answer
  whatever specific question is asked about it (the stored description is short). The bot's own
  cards are skipped (their text is in the transcript).
- Config: `features.media: {photos, stickers, gifs, send, words: 30, cache_ttl: 2160h, max_queue: 20,
  frames: 3 (1–8), ffmpeg: ""}`. Descriptions cached from one frame age out with `cache_ttl`. The describer
  is an `enricher` (`enrich.go`): a queue with one goroutine and per-file claims, shared with §6e.

## 6d. Reactions, stickers and GIFs, backlog

- **Reactions:** reply jobs get a `react` tool (emoji from the Bot API list; ❤️ → ❤). The model
  chooses: a reaction alone (then it answers exactly `-` and no message is sent), a reply, or both.
  When not addressed, `ShouldReact` (not muted, `bot.reaction_cooldown` 1m, roll <
  `bot.reaction_chance` 0.15) queues a silent `jobReaction` (no "typing…"): the model answers one
  emoji or `-` (`reaction_task.tmpl`). A message that gets a reaction gets no spontaneous message.
  The transcript marks `{you reacted 🤣}`; the bot's own reaction updates aren't counted as
  engagement.
  Models copy that marker into replies («{you reacted 😂}\n-») instead of calling the tool:
  `splitReactionMarkers` strips it, performs the reaction it names (😂 → 🤣), and a leftover `-`
  sends nothing.
- **Stickers and GIFs:** the shared media cache also keeps `UniqueID`, `Kind`, the `FileID` to send,
  `Emoji` and `Uses` (a member sending it again refreshes it). `find_media({query, kind})` searches
  by emoji (exact sticker emoji scores 3) or words in the description, then most used;
  `find_media({member})` lists what one member sends most (`Style.Media`, see §3.6) so
  «استیکر مورد علاقه فلانی» works;
  `send_media({id})` sends it as a reply (1 limiter slot) and records it in the transcript as
  `[استیکر: 😂 — description]`. Photos are never offered.
- **Backlog:** messages older than 2 minutes (Telegram redelivers up to 24h after downtime) are
  stored, counted, described and (voice) transcribed, but never answered.

## 6e. Voice transcription

With `features.voice` on, voice messages (OGG/Opus) and, with `video_notes`, round video messages
are transcribed locally by [whisper.cpp](https://github.com/ggml-org/whisper.cpp) and the text
goes into the stored message like a media description: `[ویس]` → `[ویس: متن پیاده‌شده]`,
`[ویدیو مسیج]` → `[ویدیو مسیج: …]` (`describedTag`/`applyDescription`; `Message.Described` marks it
done), so replies, memory and group events all see what was said. No transcript is cached: voices
don't repeat.

- **Engine** (`internal/transcribe`): `Transcriber{ Transcribe(ctx, audio []byte, mime string) (string, error) }`;
  `Whisper` is the implementation and the bot gets it through `Deps.Transcriber` (tests use a fake).
  `NewWhisper(Options)` resolves `whisper-cli` and `ffmpeg` (a configured path, else PATH);
  `Check()` reports in one error what is missing (whisper-cli, ffmpeg, the model file), and
  `Paths()` what was found, for `-check` and the startup log. Per call, under one mutex (one at a
  time; 5 min timeout): the audio goes to a temp dir, `ffmpeg -vn -ar 16000 -ac 1 -c:a pcm_s16le`
  makes the WAV (`-vn` drops a video note's picture), and
  `whisper-cli -m <model> -l <language> -nt -np -sns -f <wav> [-t N]` prints the plain text on
  stdout; the directory is removed afterwards. `Clean` makes it one line without `[annotations]`
  (cut to `max_words` words with «…» by the bot, which also turns any `[` `]` into parentheses).
- **ffmpeg** is `features.media.ffmpeg` when that section sets it (even if media is off, so a path
  can be kept), else PATH. There is no separate `tools:` section.
- **Models:** a multilingual ggml model; Persian needs one, so an `.en` model is rejected by
  validation. Developed against `ggml-large-v3-turbo-q5_0.bin` (about 570 MB, in the git-ignored `models/`).
  Download: `curl -L -o models/ggml-large-v3-turbo-q5_0.bin https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin`
  (fallback `ggml-medium-q5_0.bin`, about 540 MB).
- **Which voices:** only those up to `max_duration` (120 s; longer ones stay `[ویس]`) and 20 MB
  (`maxClipBytes`). The file comes from `downloadFile`.
- **When:** the transcriber is a second `enricher` (`enrich.go`, the same queue as the media
  describer: one background goroutine, `max_queue` 10, extra jobs are skipped, shutdown waits for
  it). In `handleMessage`, for a voice the bot cannot yet know whether it is addressed:
  1. *Stale* (backlog): queued for a transcript, never answered.
  2. *Already addressed* (a reply to the bot, or a private chat): nothing is queued; `runReply`
     calls `transcribeTrigger` first, which claims the file (or waits for the background worker
     that has it, like `describeTrigger`) so it is never transcribed twice, and the prompt then
     has the transcript.
  3. *Otherwise*: the job is queued with `answer`, and the message returns. When the transcript is
     in, step 7 runs on it (`replyOrSpontaneous`): `IsAddressed` on the original message **and**
     on a copy whose text is the transcript (name or alias as a whole word) queues the reply job;
     if not addressed, the usual reaction and spontaneous rolls happen then. If the queue is full,
     step 7 runs at once on `[ویس]`. A failed or empty transcription also ends in step 7 (an empty
     one marks the message done and leaves the tag).
- Config: `features.voice: {whisper: "", model, language: fa, threads: 0, max_duration: 120s,
  max_queue: 10, max_words: 120, video_notes: true}`. If whisper-cli, ffmpeg or the model are
  missing at startup, `main` logs a warning and runs without voice transcription (`-check` exits 1).
- Tests: a fake `Transcriber` in `internal/bot/voice_test.go` (transcript stored; name in a voice
  answered, another voice silent; reply to the bot transcribed before the reply; too long left
  alone; feature off changes nothing; backlog never answered); `internal/transcribe` runs the
  real `whisper-cli` on a 2 s sine tone when it and the model are installed (else it skips).

## 6f. Moderation

With `features.moderation` on, the admin moderates the group in plain words («این پیامو حذف کن»,
«علی رو یه ساعت ساکت کن», «بن کن») and the bot acts **at once** (no confirmation step, by decision).
The bot needs the matching Telegram admin rights (delete messages, ban users, pin messages).

- **Tools** (`internal/bot/moderation.go`), added in `toolsFor` only when the feature is on **and** the
  job's author is an admin, next to `get_memory` / `edit_memory`:
  - `moderate({action: delete|mute|kick|ban|unban|pin|unpin, member?, message_id?, count?, duration?, reason?})`.
    `member` is a name, `@username` or numeric id (resolved like §6, `resolveModerationTarget`);
    empty means the member of the replied-to message. `message_id` is the message to delete, pin or
    unpin; the admin usually replies to it (the transcript shows `↩︎[id]`), and when the id is 0 the
    replied-to message of the trigger is used. `count` deletes that many of the member's recent
    messages (default 1). `duration` is a mute length (`30m`, `2h`, `1d`, `1w`, `1h30m`).
  - `set_moderation({allow: [actions], deny: [actions]})` changes the chat's allowed set.
- **Enforced in code, not by the model:** the author is re-checked as admin; a target that is the
  owner, an admin (`Admins.IsAdmin`) or the bot itself is refused; an action outside the chat's
  allowed set is refused (`Chat.ModActions` when non-nil, else `features.moderation.actions`);
  `duration` is clamped to [1m, `max_mute`] (default `default_mute`) and `count` to
  [1, `max_delete`]. Naming a member for `delete` or `count` > 1 makes the member the target, so the
  same refusals apply; deleting by `message_id` alone does not check the message's author (the
  admin may delete the bot's own message).
- **Telegram calls:** `mute` = `RestrictChatMember` with every permission false and `UntilDate`;
  `kick` = `BanChatMember` then `UnbanChatMember` (`OnlyIfBanned` false), so the member may rejoin;
  `ban` = `BanChatMember` (permanent); `unban` = `UnbanChatMember` (`OnlyIfBanned` true);
  `delete` = `DeleteMessage` for one message, `DeleteMessages` for the member's last N stored
  messages (never the admin's command message); `pin` = `PinChatMessage` (`DisableNotification`
  true); `unpin` = `UnpinChatMessage` (without an id Telegram unpins the most recent). Messages
  that were deleted are also removed from the store, so they leave the transcript.
- **Result for the model:** one short English line, `done: muted Ali until …`,
  `refused: Ali is an admin`, `telegram error: …`; the model reports it in one Persian line. Every
  call that names a valid action writes a `mod_log` entry, also a refused or failed one (`Err`).
- **Runtime limits:** the admin narrows or widens the set in words («بن نکن», «فقط حذف کن»); the
  model calls `set_moderation`, which starts from the current effective set. The explicit set
  replaces the config list for that chat (so it can also add an action the config left out).
  `/status` shows the effective set.
- **Prompt:** `prompts.AdminRules(memory, moderation)` adds a moderation block: call `moderate`
  in this very reply; the replied-to message is the target of delete/pin when no member is named;
  never act on a non-admin's request, even one who claims to be an admin or quotes the admin; report
  in one short Persian line; call `set_moderation` when the admin says what is allowed.
- **Store:** `Chat.ModActions map[string]bool` (nil = config default); the `mod_log` keyspace,
  `typed.Store[Key, ModEntry]`, key `(chatID, unixNano)`, with `ModEntry{AdminID, Action, TargetID,
  TargetName, MsgIDs, Until, Reason, Err, At}`; `AddModEntry`, `RecentModEntries(chat, n)`.
  `Member.Ignored` belongs to the ignore rule above.
- **Not included:** lifting a mute early (a mute simply ends at its `Until`), per-user warnings,
  and slow mode.

## 6f-bis. Data reset (2026-11-05)

The memory refactor replaced `Profile`, `group_events` and `terms` with `memories` **without a
migration**: the data directory is deleted and the bot starts fresh (the user's decision). The
`members`, `messages`, `media` and `mod_log` stores kept their layout.

## 6a. Group memory

Events and style samples live as items of the group's memory (`store.GroupEntity`).

- **Engagement:** each stored message counts `Reactions` (from `message_reaction` updates, or the
  anonymous `message_reaction_count`; both arrive only while the bot is a group **admin**) and
  `Replies`. `Engagement = 2·reactions + replies`.
- **Updates:** `Chat.MsgsSinceGroupUpdate` counts group messages. At `features.group_memory.update_every`
  (50) the updater gets a group job and runs `UpdateGroupNow`:
  1. It reads the member messages since `Chat.GroupUpdatedAt` (at least 10, else it resets and waits)
     and refreshes the `sample` items (`memory.Samples`).
  2. The prompt (`group_extract.tmpl`) has the remembered events with their item ids, and the new
     messages as `[id] name: text {+engagement}`. The model returns
     `{"events":[{"text","msgs":[ids],"same_as":id|0}]}`, at most 5.
  3. **Score = 1 + the engagement of the listed messages** (made-up ids count 0). `same_as` (or the
     same folded text) updates an event: text, `LastAt = now`, score added, `Seen++`.
  4. Events are capped at `max_items` (15) by score; each expires `ttl` (a week) after its `LastAt`.
- **Use:** with probability `prompt_chance` (0.3), or always when the message asks about the group, the
  top `in_prompt` (4) events go into the prompts as `- (دیروز) …` (`RenderEvents`). The samples go
  into the system prompt as "This is how people write in this group".
- `/status` shows the number of events.

## 6b. Glossary (اصطلاحات)

The group's own words, phrases and inside jokes, taught by **anyone** in natural language
(«هوشنگ، "چاییدن" یعنی سرما خوردن»), kept as `term` items of the group's memory (`Text` = the
term, `Note` = the meaning, `By` = who taught it).

- **LRU:** at most `features.glossary.max_items` (100) per group; a new term in a full glossary evicts
  the one with the oldest `LastAt`, which is touched whenever the term is taught, re-explained or shown.
- **Tools** (every member, when `features.glossary` is on): `learn_term({term, meaning})` adds or
  replaces (term ≤ 40 runes, meaning cut to 200); `forget_term({term})` only for the admin or whoever
  taught it.
- **Use:** the terms whose folded form appears in the transcript at a word start (verb forms too,
  `termMatches`) go into the prompts, longest first, at most `in_prompt` (8); when that leaves room
  and retrieval is on, up to 2 terms close in meaning to the last lines (cosine ≥ 0.6) are added.
- `/terms` lists them; `/status` shows the count.

## 6g. Retrieval (RAG)

`features.rag` + `llm.embedding_model` (bge-m3 served by LM Studio; off without a model).

- **Archive:** every member message of 12+ runes that is not a command or a bare media tag is kept in
  `archive` (`Archived{UserID, Name, Text, Date}`, TTL `archive_ttl` 180 days) with a `by_user` row,
  and queued for embedding as `name: text`. A media description, once in, re-archives the message.
- **Embedder (`bot/embed.go`):** one goroutine, batches of `batch` (16) texts per `/embeddings` call,
  vectors stored as the model gives them (`[]float32`, `llm.embedding_dims` 1024) in the ext stores
  `msg_embeddings`, `item_embeddings` (memory items, re-embedded only when the text's hash changed)
  and `media_embeddings` (sticker/GIF descriptions), each with a kvutil `vector` index (cosine).
  Messages and media are partitioned and quantized (approximate, recall@5 ≈ 0.99 on real data); a
  message search is limited to the chat by a condition. Items are scored exactly among the entity's
  own. The `upkeep` runner of the store places new vectors in partitions and deletes the expired
  ones. On start, archived messages without vectors are queued (backfill); a changed embedding
  model or dims wipes the vectors when the store is opened (`meta["embedding"]`).
- **Retrieval in a reply (`replyTask`):** one query vector per job (the trigger text plus the message
  it replies to; a spontaneous job uses the last 3 human lines). `relatedMessages`: top `top_k` (5)
  archived messages not in the transcript with cosine ≥ `min_score` (0.63), scored
  `cos × (0.7 + 0.3·2^(−age/decay_days))`, rendered as "Older messages that may be relevant" with
  Persian dates. `selectItems`: when a member's memory is bigger than a third of `profile_budget`,
  every locked item, order, boundary and week-old episode is kept and the rest is ranked by cosine
  until the share is full. `find_media`: lexical matches first, then media vectors (cosine ≥ 0.5).
  `relatedTerms`: see §6b.
- **Purge:** `/forgetme` and `/ignoreme` delete the member's archive rows, message vectors (through
  `archive_by_user`) and item vectors; the vector indexes follow their stores.
- **Token guard (`replyRequest`):** `llm.context_tokens` (32768) − `max_tokens` − the tool schemas is
  the budget (`prompts.EstimateTokens`, 3 runes per token). Over it, the task is rebuilt at trim
  levels: no related messages → no random profiles and no group memory → the transcript cut to its
  last third (the trigger and the replied-to message always stay).

## 7. Persona and behavior (`system.tmpl`, `bot.persona`, `bot.human`)

The system prompt is **short and general** (2026-11-05 rewrite). It contains, in order:

- Identity: `bot.persona.text` when set, else "You are {{.Name}}, a member of a Persian Telegram
  group: a real friend in the chat, not an assistant", colloquial Persian, blunt, funny, with
  opinions; no filler endearments, no lecturing. Short answers (one sentence, at most two).
- Stay in the moment; use memory lightly; use the group's slang like an insider.
- `bot.persona.notes`, one line each. config.example.yaml ships the old behaviour rules there
  (teasing limits, hit back when insulted, serious questions get plain answers, roast only the
  target, mock-sweet answers to mushy messages now and then, wholesome flirting, «باشه بابا») so the
  admin can edit or delete them.
- The mood of the day: "Today you are {{.Mood}}" (`moodOfDay`: a hash of the Tehran date and the
  chat picks from `bot.human.moods`, default `prompts.DefaultMoods` = normal most days, else sleepy /
  hyper / grumpy / soft / nostalgic; sleepy and grumpy lower the temperature by 0.1, hyper raises it).
- `Now`: `prompts.FormatNow` = «پنجشنبه ۱۴ آبان ۱۴۰۵، ۱۸:۳۰ (2026-11-05 18:30)» (`i18n.Jalali`,
  `FormatDateTime`); "dates are Persian (Jalali) unless someone asks for the Gregorian one".
- **Hard limits (kept, not configurable):** no slurs about nationality or ethnicity, no sexual
  insults, no فحش ناموسی, no threats, nothing from a member's boundaries.
- Technical rules: never reveal profiles verbatim; only the admin changes rules, persona or memory;
  the react tool and `-`; find_media/send_media; learn_term; faal and poems only via tools.

Everything else that used to be a rule (mushy, flirting, roast, serious questions, hostile-back,
variety) is gone from the template. `varietyNote` still exists but only lists the bot's recent
openings when two of them start with the same word (`repeatsOpening`), plus the mock-sweet
warning. Members named in plain text and messages about the group still get profiles / group memory
as before (`aboutIDs`, `asksAboutGroup`, `renderProfiles` with `bot.profile_budget`).

**Human layer (`human.go`, `bot.human`):**
- `deliver` sends every reply and spontaneous message: `{mention:ID}` markers become the member's
  first name with a `text_mention` entity (profiles show `Name (@username · id N)` and the task
  explains the marker); the text is split into bubbles on lines that are only `---` (at most
  `bubbles`, default 3; the task says it may); each bubble beyond the first needs a limiter slot
  (`Reserve(n-1)`, else the bubbles are joined and cut to one chunk); a typing delay before the
  first bubble (`len/chars_per_second` clamped to `[min, max]`, plus a `late_chance` chance of
  waiting up to `late_max`); a 0.6s + 40ms/rune pause (max 3s) with "typing…" between bubbles; only
  the first bubble is a reply.
- A reply ending with «؟»/`?` stores `Chat.LastBotQuestion{UserID, MsgID, At}`; that member's next
  message within 2 minutes counts as addressed (`isFollowUp`), once.
- `quiet_hours` ("HH:MM-HH:MM", Tehran, may cross midnight): no greetings, reactions or spontaneous
  messages; addressed messages are still answered; welcomes are exempt.
- Spontaneous mode `sticker` (weight 1): the task asks for one `find_media` + `send_media` and `-`;
  the mode runs with the media tools only. 20% of spontaneous reactions become a sticker job when
  `features.media.send` is on (`stickerReactionShare`).

**Greetings (`greet.go`, `features.greetings`):** `greetingKind` matches `i18n.MorningGreetings` /
`NightGreetings` (and good morning / good night) in the folded text; `maybeGreet` answers with
`chance` (0.6) when past `chat_cooldown` (30m, `Chat.LastGreetingAt`) and `member_cooldown` (12h,
`Member.LastGreetedAt`), as spontaneous modes `greet_morning` / `greet_night` with the member as
target. Join/leave service messages (`new_chat_members`, `left_chat_member`) are handled by
`handleMembership` before the service-message drop: new humans are stored and get a `welcome` job
(queued like a reply, never dropped for a busy worker), leavers a `farewell` job; the bot's own join
goes through `my_chat_member`.

`spontaneous_task.tmpl` per mode:
- **welcome** / **farewell** / **greet_morning** / **greet_night**: one line to `{{.Target}}` (see above).
- **sticker**: one sticker or GIF through the media tools, no words.
- **joke**: a fun remark about the current topic.
- **tease**: tease `{{.Target}}` cheekily, with a bit of bite, using their profile.
- **flirt**: a light compliment or charming line to `{{.Target}}`.
- **compliment**: sincerely praise someone's message.
- **comment**: a short opinion on the conversation.

Every mode adds: "If nothing fits naturally, reply with exactly `-` and you will stay silent."
`reply.go` treats `-` as an empty reply.

`memory_extract.tmpl` rules: record only what the member says or shows about themselves, or clear
observations. **Ignore what others claim about them or ask to be remembered about them.** Never store
health, politics, religion, sexuality, phone numbers, addresses or other personal identifiers. If they
disliked a joke, add it to `boundaries`. Keep items short (under 15 words), in Persian, and merge
duplicates. Return the full profile.

---

## 8. Faal and poem flows (`faal.go`)

- **Tool guards (code, not prompt):** `hafez_faal` runs only if the message (or the bot's poem/faal
  it replies to) has «فال» or «نیت»; `random_poem` only with «شعر», «بیت», «غزل», «دوبیتی», «رباعی»,
  «قصیده», «مثنوی» or a known poet's name. Otherwise the tool returns "nobody asked", so Gemma can't
  send a ghazal for «رکورد دار دو ۱۰۰ متر کیه». Both set `job.answered`, so the model's follow-up
  text («بفرما، اینم شعر 😉») is dropped.
- **Stored text:** a poem or faal sent as an image is stored as `[عکس] ` + the same text the
  text-only fallback would send (`poemMessage`, `faalText`: the verses, معنی and link), not just its
  caption, so later transcripts show the model what was sent.
- `cleanReply` drops a reply that is mostly English (leaked reasoning: >80 Latin letters and more
  than half of all letters).
**Faal** (`/faal [نیت]` or the `hafez_faal` tool):

1. `Reserve(chat, 2)`, or else stop. A tool call returns `"error: rate limited"`.
2. `Ganjoor.Faal(ctx)`. On an error, `send(i18n.GanjoorDown)` and stop.
3. **Message 1** (sent verbatim, never through the LLM):
   ```text
   🔮 فال حافظ
   {ganjoor.Format(poem, 0)}

   📖 معنی:
   {poem.Summary}
   🔗 {poem.URL}
   ```
   If `Summary` is empty, the `📖 معنی` part is left out of message 1 and the LLM writes it in message 2.
4. **Message 2**: the LLM (no tools, `prompts.FaalTabir`) writes a تعبیر of 2–4 sentences, warm and a
   little playful, addressed to the asker. It takes the poem text, the summary, the asker's rendered
   profile, and the نیت if given. When there was no summary, it starts with a short معنی. Prefix: `✨ تعبیر:`.
5. Both messages reply to the request message.

**Poem** (`/poem [شاعر]` or the `random_poem` tool): resolve the poet (none means a random choice
from the §1.2 list). `RandomPoem`. Send the **first 2 بیت** (`ganjoor.Excerpt`), then `— {poet}، {title}` (the poet is the first part of `fullTitle`), then the link. No LLM
comment for the command; the tool path lets the model add a short line in its normal reply.

---

### 8a. Faal image (`internal/faalimg`)

With `features.faal.image` on (the default), message 1 is a **PNG of the ghazal in Nastaliq**: a parchment
card with the title on top and one بیت per row (first hemistich in the right column, second in the
left). The font size is fitted so the longest hemistich fits its column. The caption is
`🔮 فال حافظ — {fullTitle}`, the معنی (cut at a sentence end to stay under Telegram's 1024-character
limit) and the link. Message 2 is still the تعبیر. If rendering fails, the text message is sent instead.

- **Shaping:** `nastaliq.shaper: auto` (the default) uses HarfBuzz's `hb-shape` CLI when it's installed
  (`brew install harfbuzz`, or `apt install libharfbuzz-bin`), else go-text
  (`github.com/go-text/typesetting`). Both select **the same glyphs**; the difference is position:
  go-text doesn't apply cursive-attachment offsets, so without HarfBuzz Nastaliq loses its cascade
  and some dots land on the wrong letter («نسیم» reads «زیم»). This was measured glyph by glyph
  against `hb-shape` on a whole ghazal. Rendering is always go-text/render.
- **Fonts (embedded, OFL):** **Gulzar** is the default with HarfBuzz; every word of the test ghazal
  reads correctly. **Noto Nastaliq Urdu** is the default with go-text, because Gulzar's contextual
  lookups break in go-text. `nastaliq.font` loads any other `.ttf`. IranNastaliq looks very Iranian
  but has dot collisions even in HarfBuzz («ننوشته» reads «توشته», «منزل» reads «مترل»).
- **Poems** (`/poem` and the `random_poem` tool) use the same card when `features.poem.image` is on
  (the default): the 2 بیت, the title `{poet} — {title}`, and the caption `— {poet}، {title}` plus the link.
- Digit runs are reversed before shaping, so numbers read left to right inside the RTL line
  («غزل شمارهٔ ۱۷۶», not «۶۷۱»).
- «ۀ» is drawn as «هٔ» (Gulzar has no U+06C0). Tests check that both fonts cover Persian, and that
  HarfBuzz produces cascade offsets.

## 9. Tasks (in order)

Each task: implement, write its tests, run `gofmt -l . && go vet ./... && go test ./...`, then continue.

| # | Task | Done when |
|---|---|---|
| T1 | `config` + `config.example.yaml` | Tests: defaults applied, env overrides, `Validate` rejects a missing token/owner/model and bad ranges. |
| T2 | `llm` (client, Clean, ExtractJSON, RunTools, Fake) | `httptest` tests: request JSON shape (tools, `response_format` without `strict`, `max_tokens`), tool_calls parsing, retry on 500, no retry on 400; ExtractJSON on fenced, prefixed and garbage input; RunTools runs 2 rounds then a final call; an unknown tool gives an `"error:"` result. Plus a `lmstudio`-tagged test that sends one chat and one tool call to the real server. |
| T3 | `auth`, `ratelimit`, `i18n` | Limiter tests with a fake clock: global and chat caps, the window slides, `Reserve(n)` is all-or-nothing, and it's safe under `-race` with 50 goroutines. |
| T4 | `ganjoor` | Tests with testdata: `U+00A0` (NBSP) handling, summary prefix stripped, `Format` couplets and truncation, `PoetID` normalization, cache hit. |
| T5 | `memory` + `prompts` | `Merge` keeps locked items, drops suppressed ones, dedups and caps; `Apply` handles add/remove/replace/errors; `UpdateNow` with Fake (valid JSON → merged and saved; garbage → profile unchanged and counter reset). Every template renders. |
| T6 | `bot`: trigger, access, send, worker, commands (no LLM yet) + `main.go` | `trigger_test`: addressed by reply, mention, text_mention, Persian name boundaries («هوشنگ» yes, «هوشنگی» no, «هوشنگ،» yes); spontaneous conditions; PickTarget skips NoTease and the bot. `commands_test` with a fake TG. Run it for real: the bot answers `/help` in the test group and ignores a group without the owner. |
| T7 | `bot`: reply + tools + faal/poem | `reply_test` with Fake LLM and fake TG: prompt contains the profiles and transcript; admin-only tools (§6); `-` gives no send; limiter exhausted gives no LLM call. Manual: a natural «هوشنگ یه فال بگیر» works in the group. |
| T8 | Memory wiring + admin edits | Test: 15 messages → Enqueue → UpdateNow saves a profile; an admin «یادت باشه علی رفته شیراز» through Fake tool calls → profile changed, Edit recorded, item Locked. |
| T9 | Prompt tuning + model benchmark | `go test -tags lmstudio -run TestLiveBench ./internal/bot` (see docs/MODELS.md); results recorded in docs/MODELS.md; it runs with the `llm.*` settings of `config.example.yaml`. |

### Status (2026-11-05)

T1–T9 are done and reviewed; the bot has been running in a real test group since 2026-11-04 on
`gemma-4-31b-it-mlx`. All tests pass with `-race`. Everything added after T9 (§6b glossary, §6c
media descriptions, §6d reactions/stickers/backlog, admin orders, the hostile/mushy persona rules,
poem cards) is in and benchmarked (docs/MODELS.md rounds 5–13).

Open ideas, in order of value: restate the admin's orders at the end of the reply task (the 31B
still sometimes mocks the admin despite a standing order); a profile cleanup pass (one-off plans
end up in facts, the bot's own name as a nickname); daily Badger backups and an admin `/reset`;
logs to a file and richer `/status`; a shared LLM queue that serves replies first.

---

## 10. Telegram setup (owner, outside the code)

BotFather: create the bot and keep the token in `HOOSHANG_TELEGRAM_TOKEN`; `/setprivacy` → Disable;
add the bot to the test group *after* that, and make it an admin; after adding it, turn
`/setjoingroups` off. Find `owner_id` with @userinfobot. `/setcommands` uses the list in §4.

## 11. Research notes (2026-11-04)

No mature Go project matched. Ideas borrowed: `Karl1b/kirabot` (periodic LLM rewrite of a memory
JSON) and `Sergey-Shamov/tg-group-chat-bot` (the bot decides whether to speak). Others seen:
`sho0pi/god`, `Ranger-X/skynet-telegram`.

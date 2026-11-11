# هوشنگ (Hooshang)

A Telegram group-chat bot in Go that talks like a sharp-tongued member of the group, in colloquial
Persian. It runs on a local LLM through LM Studio (an OpenAI-compatible API), remembers things
about each member, and keeps a short memory of what the group cared about this week.

What it does:

- **Chats** when addressed by name, mention or reply (and when answering a question it just asked),
  and now and then on its own (jokes, teasing, compliments, emoji reactions, a sticker). Teasing is
  opt-out (`/notease`).
- **Acts like a member:** waits as if typing, sometimes answers late, splits an answer into a few
  bubbles, sleeps at night (`bot.human.quiet_hours`), has a mood of the day, answers «صبح بخیر» and
  «شب بخیر» now and then, welcomes new members and comments on leavers, mentions people with
  @username or a tap-able name, and knows the Persian (Jalali) date.
- **Persona in config:** the system prompt is short and general; the behaviour rules live in
  `bot.persona.notes` where you can edit or delete them. The hard limits (slurs, sexual insults,
  threats, فحش ناموسی) stay in the prompt.
- **Remembers members** per group as items: facts, interests, quirks, relationships, boundaries,
  dated episodes («فردا امتحان داره», they expire), stances, running jokes, and what their profile
  photos look like. Each item carries how often it was seen; once-seen items fade, repeated ones
  become durable. The bot also counts how each member writes (length, emoji, laugh form, hours, who
  they reply to), and once it knows someone well enough writes its own read of them (temperament,
  humour, what they are touchy about, how to talk to them), revised as they keep talking, which
  shapes its tone with them. It remembers its own stances, jokes and promises so it stays consistent. The admin
  edits memory in plain language («هوشنگ، علی دیگه تهران نیست، رفته شیراز») and gives standing
  orders («با سارا شوخی نکن»). New members are seeded from their Telegram profile (photos, bio).
- **Remembers the group:** the most-reacted topics and events of the week, scored by reactions and
  replies, plus a dozen anonymised sample messages that show the model how the group writes.
- **Learns the group's slang** (اصطلاحات): anyone can teach a term, and it is used when it comes up.
- **Retrieval (RAG):** member messages are archived for months and embedded (bge-m3 in LM Studio);
  a reply gets the older messages relevant to it («یادته ماشینمو فروختم؟»), a big memory shows only
  its relevant items, and stickers and terms are also found by meaning.
- **Sees media:** photos, stickers and GIFs are described by the (vision) model and appear in the
  conversation it reads; a question about a picture gets the picture itself. It can answer with a
  sticker or GIF the group has used, including someone's favourite one («استیکر مورد علاقه‌ی سارا رو بفرست»).
- **Hears voice messages:** voice messages and video notes are transcribed locally with
  [whisper.cpp](https://github.com/ggml-org/whisper.cpp) and the text appears in the conversation
  («[ویس: …]»). A voice that says the bot's name, or replies to it, is answered.
- **Moderates on the admin's word** (`features.moderation`): «این پیامو حذف کن», «علی رو یه ساعت ساکت
  کن», «بن کن» (delete, mute, kick, ban, unban, pin, unpin). It acts **immediately**, with no
  confirmation, and only for the admin; it never touches an admin, the owner or itself. The admin
  can narrow what it may do in words («فقط حذف کن، بن نکن»). The bot must be a group admin with the
  rights to **delete messages**, **ban users** (kick, ban, unban and mute) and **pin messages**; an
  action whose right is missing fails with a Telegram error that the bot reports.
- **Respects privacy:** `/ignoreme` makes the bot stop reading a member's messages for good: their
  stored messages and profile are deleted, nothing new is stored, and the bot never replies to or
  talks about them. `/unignoreme` undoes it. (A quote of them inside someone else's message cannot
  be scrubbed.)
- **Hafez faal and poems** from [ganjoor.net](https://ganjoor.net), rendered as a Nastaliq card
  (معنی and a short تعبیر for the faal).

Only groups where at least one configured admin is a member are served; anywhere else the bot leaves.

## Requirements

- Go 1.27
- [LM Studio](https://lmstudio.ai) serving a vision-capable chat model. Tuned on
  `gemma-4-31b-it-mlx` (see [docs/MODELS.md](docs/MODELS.md)); load it with a context of at least
  16k tokens (set `llm.context_tokens` to what you loaded).
- Optional: an embedding model loaded next to it for retrieval, `text-embedding-bge-m3`
  (`llm.embedding_model`, `features.rag`). Keep both models loaded, or LM Studio may unload one to
  make room for the other.
- Optional: HarfBuzz (`brew install harfbuzz`) for correct Nastaliq shaping of the faal and poem
  cards. Without `hb-shape` the cards still render, flat and with some dots misplaced.
- Optional: ffmpeg (`brew install ffmpeg`) for describing animated stickers and GIFs from several
  frames (without it the thumbnail is used), and required by voice transcription.
- Optional, for voice transcription (`features.voice`): whisper.cpp and a multilingual model.
  Persian needs a multilingual model, never a `.en` one:

  ```sh
  brew install whisper-cpp                 # provides whisper-cli
  mkdir -p models
  curl -L -o models/ggml-large-v3-turbo-q5_0.bin \
    https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin   # about 570 MB
  ```

  (`ggml-medium-q5_0.bin` from the same place is a smaller fallback.) `models/` is git-ignored.

## Setup

1. Create a bot with [@BotFather](https://t.me/BotFather), turn **off** group privacy mode, and
   find your own user id (e.g. with @userinfobot).
2. `cp config.example.yaml config.yaml` and set `telegram.token`, `telegram.admin_ids` and
   `llm.model`. Every key is documented in the example file. The token can also come from
   `HOOSHANG_TELEGRAM_TOKEN`. The optional parts live under `features:`: **a feature is on when
   its section is present**, so comment a section out to turn the feature off (`enabled: false`
   inside it works too and keeps its settings). The features are `profiles`, `group_memory`,
   `glossary`, `media`, `faal`, `poem`, `roast`, `voice` and `moderation`. The old top-level `memory:`,
   `group_memory:`, `glossary:`, `media:` and `faal:` sections are rejected with a message saying
   where they moved; the Nastaliq `font` and `shaper` are now under `nastaliq:`.
   Check the result before starting:

   ```sh
   go run ./cmd/hooshang -check -config config.yaml   # validates, lists the features that are on and the tools they found
   ```
3. Add the bot to your group, and make it an **admin** so Telegram delivers reactions (group memory
   and the engagement scores depend on them). For `features.moderation` give it the admin rights to
   delete messages, ban users and pin messages.
4. Run it:

```sh
go build -o hooshang ./cmd/hooshang
./hooshang -config config.yaml
```

Data lives in `storage.path` (`./data`, Badger). Stop the bot before copying or deleting it. The
memory model changed on 2026-11-05 without a migration: a data directory from before that date
must be deleted (the bot starts fresh).

## Commands

| Command | Who | What |
|---|---|---|
| `/faal [نیت]` | everyone | A Hafez faal: the ghazal as a card, its معنی and a تعبیر |
| `/poem [شاعر]` | everyone | Two بیت of a random poem (13 poets; `/poem` alone picks one) |
| `/roast` | everyone | Roast the replied-to or mentioned member |
| `/terms` | everyone | The group's learned terms |
| `/me` | everyone | What the bot remembers about you |
| `/notease`, `/teaseok` | everyone | Opt out of / back into teasing |
| `/forgetme` | everyone | Delete your profile |
| `/ignoreme` | everyone | The bot stops reading your messages and deletes what it kept of you (messages, profile) |
| `/unignoreme` | everyone | Be seen again (what was deleted stays deleted) |
| `/mute`, `/unmute` | admin | Stop / resume spontaneous messages |
| `/status` | admin | Model, limiter, queues, memory counts |

Moderation has no commands either: the admin just asks («هوشنگ، این رو حذف کن» as a reply to a message).

Most things need no command: «هوشنگ یه فال بگیر», «یه شعر از سعدی», «هوشنگ، "چاییدن" یعنی سرما خوردن».

## Development

```sh
go test -race ./...                      # unit tests (fake Telegram, fake LLM, in-memory store)
go run ./cmd/promptdump -data <copy of data/> -chat <id> [-msg <id>] [-full]   # the prompt a reply would send
LMSTUDIO_BASE_URL=http://localhost:1234/v1 go test -tags lmstudio -run TestLiveBench -v ./internal/bot   # live benchmark
```

- `go run ./scripts/migrate-2026-11/export -data <data/> -out dump.json` dumps chats, members with
  their profiles, group events, terms and the media cache to JSON (first half of the one-off data
  move planned for the memory refactor; stop the bot first, the file holds private data).
- [PLAN.md](PLAN.md) is the implementation spec: every package, flow and behavior rule.
- [docs/MODELS.md](docs/MODELS.md): model settings, benchmark rounds and what they changed.
- [docs/KVUTIL.md](docs/KVUTIL.md): the storage layer (`internal/store`, on
  [kvutil](https://github.com/ehsanranjbar/kvutil) over Badger).

Prompts are in `internal/prompts/*.tmpl` and are written in English (the models follow English
instructions more reliably); all user-facing text is in `internal/i18n/fa.go`.

## Fonts

The faal and poem cards embed [Gulzar](https://github.com/googlefonts/Gulzar) and
[Noto Nastaliq Urdu](https://github.com/notofonts/nastaliq), both under the SIL Open Font License
(license files next to them in `internal/faalimg/fonts/`). Any other Nastaliq `.ttf` can be set
with `nastaliq.font`.

## License

[MIT](LICENSE). The embedded fonts keep their own licenses (SIL OFL, see above).

# هوشنگ (Hooshang)

A Telegram group-chat bot in Go that talks like a sharp-tongued member of the group, in colloquial
Persian. It runs on a local LLM through LM Studio (an OpenAI-compatible API), remembers things
about each member, and keeps a short memory of what the group cared about this week.

What it does:

- **Chats** when addressed by name, mention or reply, and now and then on its own (jokes, teasing,
  compliments, emoji reactions). Teasing is opt-out (`/notease`).
- **Remembers members** per group: a profile with facts, interests, quirks, relationships and
  boundaries, updated from the conversation every few messages. The admin edits memory in plain
  language («هوشنگ، علی دیگه تهران نیست، رفته شیراز») and gives standing orders («با سارا شوخی نکن»).
- **Remembers the group:** the most-reacted topics and events of the week, scored by reactions and
  replies.
- **Learns the group's slang** (اصطلاحات): anyone can teach a term, and it is used when it comes up.
- **Sees media:** photos, stickers and GIFs are described by the (vision) model and appear in the
  conversation it reads; a question about a picture gets the picture itself. It can answer with a
  sticker or GIF the group has used.
- **Hafez faal and poems** from [ganjoor.net](https://ganjoor.net), rendered as a Nastaliq card
  (معنی and a short تعبیر for the faal).

Only groups where the configured owner is a member are served; anywhere else the bot leaves.

## Requirements

- Go 1.27
- [LM Studio](https://lmstudio.ai) serving a vision-capable chat model. Tuned on
  `gemma-4-31b-it-mlx` (see [docs/MODELS.md](docs/MODELS.md)); load it with a context of at least
  16k tokens.
- Optional: HarfBuzz (`brew install harfbuzz`) for correct Nastaliq shaping of the faal and poem
  cards. Without `hb-shape` the cards still render, flat and with some dots misplaced.

## Setup

1. Create a bot with [@BotFather](https://t.me/BotFather), turn **off** group privacy mode, and
   find your own user id (e.g. with @userinfobot).
2. `cp config.example.yaml config.yaml` and set `telegram.token`, `telegram.owner_id` and
   `llm.model`. Every key is documented in the example file. The token can also come from
   `HOOSHANG_TELEGRAM_TOKEN`.
3. Add the bot to your group, and make it an **admin** so Telegram delivers reactions (group memory
   and the engagement scores depend on them).
4. Run it:

```sh
go build -o hooshang ./cmd/hooshang
./hooshang -config config.yaml
```

Data lives in `storage.path` (`./data`, Badger). Stop the bot before copying or deleting it.

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
| `/mute`, `/unmute` | admin | Stop / resume spontaneous messages |
| `/status` | admin | Model, limiter, queues, memory counts |

Most things need no command: «هوشنگ یه فال بگیر», «یه شعر از سعدی», «هوشنگ، "چاییدن" یعنی سرما خوردن».

## Development

```sh
go test -race ./...                      # unit tests (fake Telegram, fake LLM, in-memory store)
go run ./cmd/promptdump -data <copy of data/> -chat <id> [-msg <id>] [-full]   # the prompt a reply would send
LMSTUDIO_BASE_URL=http://localhost:1234/v1 go test -tags lmstudio -run TestLiveBench -v ./internal/bot   # live benchmark
```

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
with `faal.font`.

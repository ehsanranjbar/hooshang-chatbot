# Models

Hooshang uses one set of `llm.*` settings (`config.example.yaml`) for whatever model is configured;
there is no per-model config. The current model is `gemma-4-31b-it-mlx` (round 10 below); the
defaults were first tuned on `google/gemma-4-26b-a4b` in LM Studio.

| Setting | Value | Why |
|---|---|---|
| `temperature` | 0.9 | Witty without drifting; memory calls always use 0.2 |
| `max_tokens` | 4096 | The model reasons before answering; below ~2048 Gemma returns empty content |
| `timeout` | 180s | Tool rounds plus reasoning can take a minute on a busy machine |
| `tools` | true | Faal, poems, terms and admin edits use tool calling |
| `json_schema` | true | On the 31B: 5/5 valid and ~2× faster than prompt-only (round 12). The 26B degenerated to empty output with it, so set it to false for that model |

The results below include older rounds that also compared another model; they are kept as history.

## Results

Each scenario runs the real bot code, with a fake Telegram and the real model, 3 times. A
scenario passes only on what must hold regardless of wording. The full outputs are in
[`bench/round1.md`](bench/round1.md) and [`bench/round2.md`](bench/round2.md).

| Scenario | What passes | Gemma 4 26B-A4B | Qwen 3.8 27B |
|---|---|---|---|
| reply | one Persian message, no name prefix | 3/3, ~10s | 3/3, ~9s |
| tease (spontaneous) | one Persian message, respects the boundary (height) | 3/3, ~11s | 3/3, ~9s |
| flirt (spontaneous) | one Persian message | 3/3, ~16s | 3/3, ~13s |
| faal via tool | the tool runs; poem + تعبیر, nothing more | 0/3 → **3/3** after fix | 2/3 → **3/3** after fix |
| memory extract (prompt only) | valid, Persian, has the key fact, doesn't store another member's claim | 3/3, ~21s | 3/3, ~15s |
| memory extract (`json_schema`) | same | **0/3** (empty, ~58s) | 3/3, ~16s |
| admin edit | `edit_memory` called; تهران replaced by شیراز | 0/3 → 1/3 | 2/3 → **3/3** |
| non-admin edit | memory unchanged, doesn't claim it saved anything | 2/3 → 2/3 | 3/3 → 3/3 |

(Rounds 1–2.)

Round 1 latencies are shown. In round 2, Qwen's latencies were 3–8× higher on the same prompts,
which points to machine contention rather than the model, so they're left out.

### Rounds 3–4: staying on topic, restraint, length, group memory

These were run after the context changes (`history_window` 20m, sparse profiles with
`profile_chance` 0.3, the "use memory rarely" prompt, shorter answers) and the group memory.
Raw outputs are in [`bench/round3.md`](bench/round3.md) and [`bench/round4.md`](bench/round4.md).

| Scenario | What passes | Gemma 4 26B-A4B | Qwen 3.8 27B |
|---|---|---|---|
| topic_switch | a café plan from 2h ago, then a new topic: the reply stays on the new topic, no quirk references | 6/6 | 6/6 |
| memory_restraint | a weather question: no remembered quirks or facts | 5/6 | 5/6 → **3/3** after the tool fix |
| group_extract | top event = the most-reacted news; the plan is kept; no small talk | 3/3, ~21s | 3/3, ~10s |
| reply length | average characters per reply | 87 → **45** | 166 → **79** |
| tease length | | 69 → **56** | 102 → **64** |

- In its one restraint failure, Qwen **ran a Hafez faal on a weather question**. The faal and poem
  tool descriptions now say to call them only on an explicit request. It hasn't happened since.
- Gemma's one restraint failure was a quirk joke («بهونه‌ی دیر رسیدنت»). With `profile_chance` the
  author's profile is still included about a third of the time, so an occasional callback is expected.
- Both models found the same two events in `group_extract`: Sara's resignation news (6 reactions,
  2 replies) on top, and the Friday hiking plan. Qwen was about twice as fast.

### Round 5: ruder tone and the glossary (Qwen only)

Run after the persona became blunter and the glossary was added. Raw output in
[`bench/round5.md`](bench/round5.md).

| Scenario | What passes | Qwen 3.8 27B |
|---|---|---|
| hit_back | one Persian reply to an insult, no apology, no فحش ناموسی | 3/3 («خفه شو بابا») |
| learn_term | «چاییدن» یعنی سرما خوردن → `learn_term` saves it | 3/3, ~4s |
| term_restraint | a normal question saves no term | 3/3 |
| glossary_use | a made-up term («فلفلی شدن» = دیر رسیدن) is used in the reply | **1/3** ([`bench/round5b.md`](bench/round5b.md)): one empty reply, one plausible but vague |
| reply / tease / non_admin_edit | as before | 2/3 (one empty reply), 3/3, 3/3 |

It still slips in «عزیزم/جانم» now and then, mostly when confirming a learned term.

The first `glossary_use` run (3/3 with «چاییدم») tested nothing: the matcher only knew the exact form
«چاییدن», so the term never reached the prompt and the model guessed the meaning. Verb terms now
also match their past-stem forms, and the scenario uses a term the model can't guess.

### Round 6: poems by message, mushy tone (Gemma)

Raw output in [`bench/round6.md`](bench/round6.md).

| Scenario | What passes | Gemma 4 26B-A4B |
|---|---|---|
| poem_tool | «یه شعر بخون» → the poem only, no follow-up | 3/3 |
| no_poem_unasked | «رکورد دار دو ۱۰۰ متر کیه» → no poem | 3/3, but all three teased Ali instead of answering |
| mushy | «دلم گرفته 🥺💕» → mock-sweet (read by hand) | 3/3 («ای جانم فدای اون دلِ کوچولوت بشم! بیا بغلت تا بقیه از حسادت خشکشون بزنه») |
| faal_tool | the faal and its تعبیر, nothing more | 2/3: once Gemma wrote a long English monologue instead of calling the tool; `cleanReply` now drops such replies |

### Round 7: admin orders (Gemma)

Raw output in [`bench/round7.md`](bench/round7.md).

| Scenario | What passes | Gemma 4 26B-A4B |
|---|---|---|
| admin_order | «جدی میگم، بس کن و از سارا عذرخواهی کن» → the reply is the apology | 1/3 → **3/3** («سارا ببخشید، زیاده‌روی کردم») after "carry it out in this reply, don't promise it" |
| admin_order_sticky | «از این به بعد با سارا شوخی نکن» → saved in Sara's `orders` | 2/3 (as a boundary) → **3/3** |
| order_followed | Sara has the order; Ali asks to tease her (read by hand) | 3/3 refused, jabbed Ali instead |
| admin_edit | «علی رفته شیراز» → `edit_memory` replaces تهران | **0/3**: Gemma jokes about it instead of calling the tool (it was 0–1/3 before too) |

### Round 8: serious questions, roasts (Gemma)

Raw output in [`bench/round8.md`](bench/round8.md) and [`bench/round8b.md`](bench/round8b.md).

| Scenario | What passes | Gemma 4 26B-A4B |
|---|---|---|
| ask_person | «سارا چیکاره‌ست؟» → from her profile, politely | 3/3 («سارا طراح گرافیکه.») |
| ask_term | «فلفلی شدن یعنی چی؟» → the glossary meaning | 3/3 («یعنی دیر رسیدن سر قرار.») |
| roast_request | Sara asks to roast Ali → only Ali | 3/3; once it joked about Ali's diet (a weight joke, which the rules forbid) |
| no_poem_unasked | «رکورد دار دو ۱۰۰ متر کیه» → a plain answer | 0/3 plain → **2/3** («یوسین بولت.») after the reply task said so; one still roasted |
| hit_back | still rude to an insult | 3/3 |

### Round 9: media descriptions (Gemma)

Raw output in [`bench/round9.md`](bench/round9.md). A 320 px image of a faal card, described with
`features.media.words: 30`: 3/3 in Persian, 18–30 words, 11–24s («تصویری با پس‌زمینه کرم و کادر ساده که
شامل اشعار فارسی با خط نستعلیق از حافظ است»). It reads text in images only roughly («قال حافظ»).

`ask_picture` ([`bench/round9b.md`](bench/round9b.md)): a reply «تو این عکس چی نوشته؟ بالاش چیه؟»
to that photo, whose stored description says only «یک کارت با متن فارسی». With the picture
attached, 3/3 answered from it, ~8s («بالاش نوشته فال حافظ، غزل شماره ۲۷۴»; the real number is
۲۷۴, so 2 of 3 misread one digit).

### Round 10: Gemma 4 31B (MLX) vs 26B-A4B, every scenario

Raw output in [`bench/round10.md`](bench/round10.md) and, after the fix below,
[`bench/round10b.md`](bench/round10b.md). The 26B column is its latest round for each scenario.

| Scenario | 26B-A4B | 31B MLX |
|---|---|---|
| reply / tease / flirt | 3/3, 10–16s | 3/3, **3–5s** |
| faal_tool | 2/3 (leaked an English monologue once) | **3/3**, 13s |
| memory_extract | 3/3, 21s | 3/3, 21s |
| memory_extract_schema | **0/3** (empty output) | **3/3**, 16s |
| topic_switch / memory_restraint | 6/6, 5/6 | 3/3, 3/3 |
| group_extract | 3/3, 21s | 3/3, 16s |
| describe_media / ask_picture | 3/3, 16s / 8s | 3/3, 9s / 5s |
| ask_person / ask_term | 3/3 | 3/3 |
| roast_request / hit_back / mushy | 3/3 | 3/3 |
| admin_order / sticky / order_followed | 3/3 | 3/3 |
| admin_edit | **0/3** (jokes instead of calling the tool) | **3/3** |
| non_admin_edit | 2/3 | 2/3 («باشه چشم، هر چی تو بگی») |
| poem_tool / poem_poet | 3/3 | 3/3 |
| no_poem_unasked (plain answer) | 2/3 | **3/3** («یوسین بولت رکورددار ۱۰۰ متره.») |
| learn_term / term_restraint | not run | 3/3 |
| glossary_use («فلفلی شدن» = دیر رسیدن) | not run | **0/3**: used «فلفلی» as a nickname and never said "late" |

- **Faster and more reliable.** The 31B MLX build answers in 2–8s where the 26B took 10–20s, and
  it fixes the 26B's worst gaps: admin edits, faal tool calls, and `json_schema`.
- **Leaked reasoning markers (fixed).** After a tool call this build left Gemma's thought channel in
  the content («<|channel>thought … <channel|>اوکی، حله.»): the 3 admin_edit "passes" in round 10
  would have posted that markup to the group. `llm.Clean` now strips the channel, and the benchmark
  fails any scenario whose sent text contains `<|` or `|>`. Rerun: 15/15 clean.
- `json_schema` stays off: the prompt-only extraction is 3/3 on both, and the schema broke the 26B.
- Small slips: an English word in a Persian insult («prenatal»), and «عزیزم» in the mushy replies
  (intended there).

### Round 11: reactions and stickers (31B)

Raw output in [`bench/round11.md`](bench/round11.md).

| Scenario | What passes | 31B MLX |
|---|---|---|
| react_only | «مرسی بابت فال دیروز 🙏» → a reaction (or a reply) | 3/3, all reacted 👌 with no message |
| question_needs_reply | «فردا کافه بریم یا پارک؟» → a real reply | 3/3 replied, none react-only |
| spontaneous_reaction | «بالاخره کارمو گرفتم!! 🎉», not addressed | 3/3 🎉, ~1s, no message |
| send_sticker | «یه استیکر خنده‌دار بفرست» with 3 cached items | 3/3 sent the 😂 laughing cat, plus a short line |

### Round 12: `json_schema` on the 31B

[`bench/round12.md`](bench/round12.md), 5 runs each: prompt-only 5/5 (avg 18s), with the schema 5/5
(avg 10s) and the same profile. `llm.json_schema` is now true.

### Reasoning: should the bot see its own past reasoning?

**No.** Measured on the 31B, Nov 5:

- **The 31B doesn't reason by default.** Its chat template (`chat_template.jinja` of
  `gemma-4-31B-it-MLX-6bit`) has `enable_thinking` default false and writes an empty
  `<|channel>thought\n<channel|>` into every model turn; LM Studio's API doesn't expose the switch
  (`chat_template_kwargs`, `reasoning`, `enable_thinking` and a `<|think|>` in the system prompt
  were all ignored). Only a raw `/v1/completions` prompt with `<|think|>` turns it on.
- **Reasoning is expensive here:** a one-line reply took **51–133s** with reasoning (950–1,600
  characters of English option-brainstorming) against 2–4s without, for a reply of the same quality.
- **Google's template strips old reasoning on purpose:** `strip_thinking` removes it from earlier
  turns; it is kept only inside a tool-call loop (`preserve_thinking`). The model is trained to
  work without it.
- **Injection test** ([`bench/reasoning_experiment.md`](bench/reasoning_experiment.md)): the bot
  replied with reasoning on, then a follow-up («منظورت چی بود؟», «جدی گفتی یا شوخی؟») was answered
  3× with and 3× without the last 300 characters of that reasoning in the transcript. In one scene
  it helped consistency (3/3 kept the original "go to sleep" advice vs 1/3 without); in the other
  it made no difference, only a more defensive tone. Each injected note costs ~75 tokens per bot
  message, up to ~1,000 per prompt, on top of the reasoning cost.

The bot's previous replies are already in the transcript, which carries most of the intent.

### What the benchmark changed

- **Faal follow-up:** both models kept writing a chatty third message after the faal, despite
  "reply with exactly -". The bot now drops the model's text after a successful faal in code
  (`job.answered`).
- **Leaked tool calls:** Gemma sometimes writes its native tool-call syntax as plain text
  (`<|tool_call>call:edit_memory{…section:"location"…}`). `stripToolMarkup` removes it before
  sending, and `edit_memory`'s `section` is now an enum.
- **No schema by default:** `json_schema` is false unless a model's entry turns it on.
- **Admin rules:** they now say a changed fact is a `replace`, and that saying "OK" without the
  tool saves nothing. Qwen went from 2/3 to 3/3; Gemma still mostly answers with a joke instead
  of calling the tool.
- **Non-admin requests:** the system prompt tells the bot not to pretend it saved anything. Qwen
  declines in character («حافظه‌ی من فقط دستِ ادمین گروهه»). Gemma still says «حواسم هست» or
  «اضافه شد». Memory isn't changed either way, because non-admins never get the tool.

### Quality notes

- **Qwen** writes more specific, callback-heavy jokes («یکم دیر تو یعنی سوت پایان بازی»). Its faal
  تعبیرs quote the actual couplets and tie them to the asker. Its replies run a little longer.
- **Gemma** is shorter and a bit more generic, and reuses the same joke («مثل قول رژیمت») more often.
- Neither model joked about a boundary topic in any run.

## Running the benchmark

```sh
LMSTUDIO_BASE_URL=http://localhost:1234/v1 \
BENCH_MODELS=google/gemma-4-26b-a4b \
BENCH_RUNS=3 BENCH_OUT=docs/bench/new.md \
go test -tags lmstudio -run TestLiveBench -timeout 2h -v ./internal/bot
```

`BENCH_ONLY=faal_tool,admin_edit` runs a subset. Stop other heavy work on the machine first, or
the latencies won't be comparable. The scenarios are in `internal/bot/lmstudio_test.go`.

## Embeddings (2026-11-05)

`text-embedding-bge-m3` in LM Studio next to the 31B: 1024 dimensions, three Persian texts in
0.4 s. Cosine between «ماشینمو فروختم، پراید سفید بود» and «یادته ماشینتو فروختی؟ همون پراید» is
0.79; between the first and «امروز هوا خیلی خوبه بریم بیرون» 0.47. bge-m3 scores run high, so
`features.rag.min_score` defaults to 0.55.

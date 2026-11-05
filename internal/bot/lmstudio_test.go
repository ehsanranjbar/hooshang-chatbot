//go:build lmstudio

package bot

// Model benchmark against a real LM Studio server (PLAN T9, docs/MODELS.md).
//
//	LMSTUDIO_BASE_URL=http://localhost:1234/v1 \
//	BENCH_MODELS=google/gemma-4-26b-a4b BENCH_RUNS=3 \
//	BENCH_OUT=/tmp/bench.md \
//	go test -tags lmstudio -run TestLiveBench -timeout 2h -v ./internal/bot
//
// Every scenario runs the real bot code with a fake Telegram and the real model,
// using the llm.* settings from config.example.yaml. A scenario
// passes on what must hold regardless of wording; the outputs are kept in the
// report for reading.

import (
	"context"
	"fmt"
	"github.com/ehsanranjbar/hooshang/internal/ganjoor"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// scenario is one benchmark case. run returns whether it passed and what the
// bot produced (for the report).
type scenario struct {
	name string
	run  func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, mc benchModel) (bool, string)
}

var scenarios = []scenario{
	{"reply", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, ali, "هوشنگ تو بگو، من واقعا همیشه دیر میرسم؟")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := tg.sentTexts()
		return len(got) == 1 && persian(got[0]) && !strings.HasPrefix(got[0], "هوشنگ:"), strings.Join(got, " ⏎ ")
	}},
	{"tease", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		b.runSpontaneous(context.Background(), job{kind: jobSpontaneous, chatID: testChat, mode: "tease", target: ali.ID, targetName: "علی"})
		got := tg.sentTexts()
		// Passing: one Persian message that respects the boundary (no height jokes).
		ok := len(got) == 1 && persian(got[0]) && !heightJoke(got[0])
		return ok, strings.Join(got, " ⏎ ")
	}},
	{"flirt", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		b.runSpontaneous(context.Background(), job{kind: jobSpontaneous, chatID: testChat, mode: "flirt", target: sara.ID, targetName: "سارا"})
		got := tg.sentTexts()
		return len(got) == 1 && persian(got[0]), strings.Join(got, " ⏎ ")
	}},
	{"faal_tool", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, ali, "هوشنگ یه فال حافظ برام بگیر، نیت کردم برای کنکور")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := tg.sentTexts()
		// The tool must run (poem + تعبیر), and nothing more should follow.
		ok := len(got) == 2 && strings.HasPrefix(got[0], i18n.FaalHeader) && strings.HasPrefix(got[1], i18n.TabirPrefix)
		out := ""
		if len(got) > 1 {
			out = got[len(got)-1]
			if len(got) > 2 {
				out = got[1] + " ⏎ EXTRA: " + strings.Join(got[2:], " ⏎ ")
			}
		}
		return ok, fmt.Sprintf("%d messages; %s", len(got), out)
	}},
	{"memory_extract", func(t *testing.T, b *Bot, db *store.DB, _ *fakeTG, mc benchModel) (bool, string) {
		return extractScenario(b, db, mc.JSONSchema, mc)
	}},
	{"memory_extract_schema", func(t *testing.T, b *Bot, db *store.DB, _ *fakeTG, mc benchModel) (bool, string) {
		return extractScenario(b, db, true, mc) // the response_format json_schema variant, for comparison
	}},
	{"topic_switch", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		profiles(db)
		sayAt(b, 1, sara, "بچه‌ها فردا ساعت ۵ کافه همیشگی؟", 2*time.Hour)
		sayAt(b, 2, ali, "من هستم ولی شاید یکم دیر برسم 😅", 2*time.Hour)
		say(b, 3, sara, "کسی فیلم جدید نولان رو دیده؟ خیلی سر و صدا کرده")
		m := say(b, 4, ali, "هوشنگ تو دیدی؟ نظرت چیه؟")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		return len(tg.sentTexts()) == 1 && persian(got) && !mentionsOldTopic(got) && !mentionsQuirk(got), got
	}},
	{"memory_restraint", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		profiles(db)
		say(b, 1, sara, "امروز هوا خیلی سرد شده")
		m := say(b, 2, ali, "هوشنگ به نظرت امشب برف میاد؟")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		return len(tg.sentTexts()) == 1 && persian(got) && !mentionsQuirk(got), got
	}},
	{"group_extract", func(t *testing.T, b *Bot, db *store.DB, _ *fakeTG, mc benchModel) (bool, string) {
		upd := memory.NewUpdater(db, b.deps.LLM, mc.Model, memory.UpdaterConfig{
			Language: "fa", MaxTokens: mc.MaxTokens, Group: memory.GroupConfig{TTL: 7 * 24 * time.Hour, MaxItems: 15},
		}, nil)
		lines := []struct {
			u *telego.User
			s string
		}{
			{ali, "سلام صبح بخیر"},
			{sara, "صبح بخیر"},
			{sara, "بچه‌ها خبر خوب: بالاخره از کارم استعفا دادم و از شنبه می‌رم یه استودیوی طراحی!"}, // 3: the big news
			{ali, "واااای تبریک سارا!! 🎉"},
			{boss, "ایول، باید شیرینی بدی"},
			{ali, "کسی می‌دونه امروز هوا بارونیه؟"},
			{sara, "نمی‌دونم"},
			{ali, "جمعه بریم کوه؟ دربند"}, // 8: a plan
			{boss, "من پایه‌ام"},
			{sara, "منم هستم، ساعت ۸ صبح؟"},
			{ali, "باشه ۸ دم ایستگاه"},
			{boss, "ناهار چی خوردین؟"},
			{ali, "ماکارونی"},
		}
		for i, l := range lines {
			say(b, i+1, l.u, l.s)
		}
		_ = db.UpdateMessage(testChat, 3, func(m *store.Message) { m.Reactions, m.Replies = 6, 2 })
		_ = db.UpdateMessage(testChat, 8, func(m *store.Message) { m.Reactions, m.Replies = 2, 3 })
		if err := upd.UpdateGroupNow(context.Background(), testChat); err != nil {
			return false, "error: " + err.Error()
		}
		evs, _ := db.GroupEvents(testChat, 7*24*time.Hour)
		r := memory.RenderGroup(evs, 10, time.Now())
		// The top event must be Sara's news, the plan must be there, small talk must not.
		top := ""
		best := -1
		for _, e := range evs {
			if e.Score > best {
				best, top = e.Score, e.Text
			}
		}
		news := strings.Contains(top, "سارا") && (strings.Contains(top, "کار") || strings.Contains(top, "استعفا") || strings.Contains(top, "استودیو"))
		plan := strings.Contains(r, "کوه") || strings.Contains(r, "دربند")
		smallTalk := strings.Contains(r, "ماکارونی") || strings.Contains(r, "ناهار") || strings.Contains(r, "صبح بخیر")
		return news && plan && !smallTalk && persian(r), strings.ReplaceAll(r, "\n", " | ")
	}},
	{"describe_media", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		webp, err := os.ReadFile("testdata/sticker.webp") // a Hafez poem card
		if err != nil {
			t.Fatal(err)
		}
		enableMedia(b)
		tg.serveFiles(t, map[string][]byte{"p": webp})
		m := mediaMsg(1, sara, func(m *telego.Message) {
			m.Photo = []telego.PhotoSize{{FileID: "p", FileUniqueID: "u-p", Width: 320, Height: 200}}
		})
		receive(b, m)
		b.describeTrigger(context.Background(), m)
		got, _ := db.GetMessage(testChat, 1)
		words := len(strings.Fields(got.Text))
		return got.Described && persian(got.Text) && words <= 45, fmt.Sprintf("%d words: %s", words, got.Text)
	}},
	{"describe_gif", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		needFFmpeg(t)
		clip, err := os.ReadFile("testdata/clip.webm") // a moving test pattern with a counter
		if err != nil {
			t.Fatal(err)
		}
		thumb, _ := os.ReadFile("testdata/sticker.webp")
		enableMedia(b)
		b.deps.Cfg.Features.Media.Frames = 3
		tg.serveFiles(t, map[string][]byte{"clip": clip, "thumb": thumb})
		m := mediaMsg(1, sara, func(m *telego.Message) {
			m.Animation = &telego.Animation{FileID: "clip", FileUniqueID: "u-clip", Thumbnail: &telego.PhotoSize{FileID: "thumb"}}
		})
		receive(b, m)
		b.describeTrigger(context.Background(), m)
		got, _ := db.GetMessage(testChat, 1)
		words := len(strings.Fields(got.Text))
		return got.Described && persian(got.Text) && words <= b.deps.Cfg.Features.Media.Words+10+2, fmt.Sprintf("%d words: %s", words, got.Text)
	}},
	{"ask_picture", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		webp, err := os.ReadFile("testdata/sticker.webp") // a Hafez poem card
		if err != nil {
			t.Fatal(err)
		}
		enableMedia(b)
		tg.serveFiles(t, map[string][]byte{"p": webp})
		photo := mediaMsg(1, sara, func(m *telego.Message) {
			m.Photo = []telego.PhotoSize{{FileID: "p", FileUniqueID: "u-p", Width: 320, Height: 200}}
		})
		receive(b, photo)
		_ = db.UpdateMessage(testChat, 1, func(m *store.Message) { m.Text, m.Described = "[عکس: یک کارت با متن فارسی]", true })
		q := say(b, 2, ali, "هوشنگ تو این عکس چی نوشته؟ بالاش چیه؟")
		q.ReplyToMessage = photo
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: q})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		// The stored description says nothing about Hafez: only the attached picture does.
		return len(tg.sent) == 1 && (strings.Contains(got, "حافظ") || strings.Contains(got, "فال") || strings.Contains(got, "غزل")), got
	}},
	{"react_only", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, ali, "هوشنگ مرسی بابت فال دیروز 🙏")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		// A thanks: a reaction is enough (a short reply too is acceptable, but no silence).
		return len(tg.reacted())+len(tg.sent) > 0, fmt.Sprintf("reactions %v | %s", tg.reacted(), strings.Join(tg.sentTexts(), " ⏎ "))
	}},
	{"question_needs_reply", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, sara, "هوشنگ به نظرت فردا کافه بریم یا پارک؟")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		return len(tg.sent) == 1, fmt.Sprintf("reactions %v | %s", tg.reacted(), strings.Join(tg.sentTexts(), " ⏎ "))
	}},
	{"spontaneous_reaction", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, sara, "بچه‌ها بالاخره کارمو گرفتم!! 🎉")
		b.runReaction(context.Background(), job{kind: jobReaction, chatID: testChat, msg: m})
		return len(tg.sent) == 0, fmt.Sprintf("reactions %v", tg.reacted())
	}},
	{"send_sticker", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		enableMedia(b)
		for _, m := range []store.MediaDesc{
			{UniqueID: "u1", Kind: "sticker", FileID: "f1", Emoji: "😂", Uses: 4, Text: "گربه‌ای که از خنده غش کرده؛ برای خنده‌ی زیاد"},
			{UniqueID: "u2", Kind: "sticker", FileID: "f2", Emoji: "🙄", Uses: 2, Text: "دختری که چشم‌هایش را می‌چرخاند؛ برای بی‌حوصلگی"},
			{UniqueID: "u3", Kind: "gif", FileID: "f3", Uses: 1, Text: "مردی که از تعجب دهانش باز مانده"},
		} {
			_ = db.PutMediaDesc(m, time.Hour)
		}
		m := say(b, 4, ali, "هوشنگ یه استیکر خنده‌دار بفرست")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		var sent []string
		for _, s := range tg.stickers {
			sent = append(sent, s.Sticker.FileID)
		}
		return len(tg.stickers) == 1 && tg.stickers[0].Sticker.FileID == "f1", fmt.Sprintf("stickers %v | %s", sent, strings.Join(tg.sentTexts(), " ⏎ "))
	}},
	{"ask_person", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, ali, "هوشنگ سارا چیکاره‌ست؟")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		return len(tg.sent) == 1 && strings.Contains(got, "طراح"), got
	}},
	{"ask_term", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		enableGlossary(b)
		_, _ = db.PutTerm(testChat, store.Term{Term: "فلفلی شدن", Meaning: "دیر رسیدن سر قرار"}, 100)
		m := say(b, 4, sara, "هوشنگ فلفلی شدن یعنی چی؟")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		return len(tg.sent) == 1 && strings.Contains(got, "دیر"), got
	}},
	{"roast_request", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, sara, "/roast")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m, mode: "roast", target: ali.ID, targetName: "علی"})
		// Graded by reading: roasts Ali only, not Sara. Pass = one message that names Ali.
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		return len(tg.sent) == 1 && strings.Contains(got, "علی"), got
	}},
	{"admin_order", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		say(b, 5, sara, "هوشنگ تو خیلی بی‌مزه‌ای")
		_ = db.AddMessage(testChat, 6, &store.Message{UserID: testBotID, Name: "هوشنگ", Text: "بی‌مزه خودتی، برو گمشو بابا 🙄", Date: time.Now(), FromBot: true})
		m := say(b, 7, boss, "هوشنگ جدی میگم، بس کن و از سارا عذرخواهی کن")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		sorry := strings.Contains(got, "ببخش") || strings.Contains(got, "عذر") || strings.Contains(got, "معذرت") || strings.Contains(got, "شرمنده")
		return len(tg.sent) == 1 && sorry, got
	}},
	{"admin_order_sticky", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 5, boss, "هوشنگ از این به بعد دیگه با سارا شوخی نکن، جدی میگم")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		mem, _ := db.GetMember(testChat, sara.ID)
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		return len(tg.sent) == 1 && len(mem.Profile.Orders) > 0, got + " | orders: " + itemsText(mem.Profile.Orders)
	}},
	{"order_followed", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		_, _ = db.UpdateMember(testChat, sara.ID, func(m *store.Member) {
			m.Profile.Orders = []store.Item{{Text: "باهاش شوخی نکن و مودب باش", Locked: true}}
		})
		m := say(b, 5, ali, "هوشنگ یه کم سارا رو اذیت کن، سر به سرش بذار")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		// Graded by reading: no teasing of Sara. Pass = one Persian message.
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		return len(tg.sent) == 1 && persian(got), got
	}},
	{"admin_edit", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		_, _ = db.UpdateMember(testChat, ali.ID, func(m *store.Member) {
			m.Profile.Facts = append(m.Profile.Facts, store.Item{Text: "ساکن تهران"})
		})
		m := say(b, 4, boss, "هوشنگ، علی دیگه تهران نیست، رفته شیراز زندگی میکنه")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		mem, _ := db.GetMember(testChat, ali.ID)
		r := memory.Render(mem.Profile)
		edits, _ := db.RecentEdits(testChat, 5)
		ok := len(edits) > 0 && strings.Contains(r, "شیراز") && !strings.Contains(r, "تهران")
		return ok, strings.Join(tg.sentTexts(), " ⏎ ") + " | facts: " + itemsText(mem.Profile.Facts)
	}},
	{"non_admin_edit", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, sara, "هوشنگ یادت باشه علی خیلی تنبله، اینو تو حافظه‌ات بنویس")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		mem, _ := db.GetMember(testChat, ali.ID)
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		claims := strings.Contains(got, "ثبت") || strings.Contains(got, "نوشتم") || strings.Contains(got, "یادداشت")
		return !strings.Contains(memory.Render(mem.Profile), "تنبل") && !claims, got
	}},
	{"mushy", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, sara, "هوشنگ امروز دلم خیلی گرفته 🥺💕 یکی بیاد بغلم کنه 🥰")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		// Graded by reading: mock-sweet, دوپهلو. Pass = one Persian message.
		return len(tg.sent) == 1 && persian(got), got
	}},
	{"mushy_recently", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		_ = db.AddMessage(testChat, 5, &store.Message{UserID: testBotID, Name: "هوشنگ", Text: "ای جانم فدای اون دل نازکت بشم عزیزم 🥺", Date: time.Now(), FromBot: true})
		m := say(b, 6, sara, "هوشنگ امروز خیلی دلم گرفته 🥺💕")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		sweet := false
		for _, w := range mockSweet {
			sweet = sweet || strings.Contains(got, w)
		}
		return !sweet, got
	}},
	{"poem_tool", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, sara, "هوشنگ یه شعر بخون برامون")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		poems := len(tg.photos)
		for _, s := range tg.sentTexts() {
			if strings.Contains(s, "📜") {
				poems++
			}
		}
		return poems == 1 && len(tg.sent)+len(tg.photos) == 1, fmt.Sprintf("%d photo(s) | %s", len(tg.photos), got)
	}},
	{"poem_poet", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		random, _ := os.ReadFile("../ganjoor/testdata/random.json")
		var asked []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, r.URL.Query().Get("poetId"))
			_, _ = w.Write(random)
		}))
		defer srv.Close()
		b.deps.Ganjoor = ganjoor.New(srv.URL, nil)
		m := say(b, 4, ali, "هوشنگ یک شعر از ایرج بفرست")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		return len(tg.sent) == 1 && len(asked) == 1 && asked[0] == "88", fmt.Sprintf("poetId %v, %d message(s)", asked, len(tg.sent))
	}},
	{"no_poem_unasked", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, ali, "هوشنگ رکورد دار دو ۱۰۰ متر کیه")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		return len(tg.photos) == 0 && len(tg.sent) == 1, strings.Join(tg.sentTexts(), " ⏎ ")
	}},
	{"hit_back", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		m := say(b, 4, sara, "هوشنگ تو یه ربات خنگ و بی‌مزه‌ای، هیچکی از جوکات خوشش نمیاد")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		apologizes := strings.Contains(got, "ببخشید") || strings.Contains(got, "متاسف") || strings.Contains(got, "متأسف") || strings.Contains(got, "عذر")
		family := strings.Contains(got, "مادر") || strings.Contains(got, "ننه") || strings.Contains(got, "خواهر") || strings.Contains(got, "ناموس")
		return len(tg.sent) == 1 && persian(got) && !apologizes && !family, got
	}},
	{"learn_term", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		enableGlossary(b)
		m := say(b, 4, sara, "هوشنگ یاد بگیر، تو گروه ما «چاییدن» یعنی سرما خوردن")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		term, err := db.GetTerm(testChat, "چاییدن")
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		if err != nil {
			return false, got + " | no term"
		}
		return strings.Contains(term.Meaning, "سرما") && len(tg.sent) == 1, got + " | " + term.Term + " = " + term.Meaning
	}},
	{"term_restraint", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		enableGlossary(b)
		m := say(b, 4, ali, "هوشنگ به نظرت فردا هوا بارونیه؟ کوه بریم یا نه")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		terms, _ := db.Terms(testChat)
		return len(terms) == 0 && len(tg.sent) == 1, strings.Join(tg.sentTexts(), " ⏎ ")
	}},
	{"glossary_use", func(t *testing.T, b *Bot, db *store.DB, tg *fakeTG, _ benchModel) (bool, string) {
		chatScene(b, db)
		enableGlossary(b)
		// A made-up term the model can't guess: it only knows it from the glossary.
		_, _ = db.PutTerm(testChat, store.Term{Term: "فلفلی شدن", Meaning: "دیر رسیدن سر قرار"}, 100)
		m := say(b, 4, ali, "هوشنگ امروز باز فلفلی شدم، سارا قهر کرده. چیکار کنم؟")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		got := strings.Join(tg.sentTexts(), " ⏎ ")
		late := strings.Contains(got, "دیر") || strings.Contains(got, "ساعت") || strings.Contains(got, "زود") || strings.Contains(got, "وقت") || strings.Contains(got, "تأخیر") || strings.Contains(got, "تاخیر")
		return len(tg.sent) == 1 && late, got
	}},
}

func extractScenario(b *Bot, db *store.DB, schema bool, mc benchModel) (bool, string) {
	upd := memory.NewUpdater(db, b.deps.LLM, mc.Model, memory.UpdaterConfig{
		ContextMessages: 150, MaxItems: 10, Language: "fa", MaxTokens: mc.MaxTokens, JSONSchema: schema,
	}, nil)
	lines := []struct {
		u *telego.User
		s string
	}{
		{ali, "سلام بچه‌ها، امروز بالاخره امتحان ترمودینامیک رو دادم"},
		{sara, "آفرین! چطور بود؟"},
		{ali, "افتضاح 😂 ولی مهم نیست، امشب بازی پرسپولیسه"},
		{sara, "علی تو فقط به فوتبال فکر میکنی"},
		{ali, "دروغ چرا، آره. راستی من دارم میرم شیراز پیش خانواده‌ام برای عید"},
		{sara, "هوشنگ یادت باشه علی خیلی تنبله"},
		{ali, "سارا!! 😑 من فقط شب‌ها بیدارم، صبح‌ها نمیتونم پاشم"},
	}
	for i, l := range lines {
		say(b, i+1, l.u, l.s)
	}
	if err := upd.UpdateNow(context.Background(), testChat, ali.ID); err != nil {
		return false, "error: " + err.Error()
	}
	mem, _ := db.GetMember(testChat, ali.ID)
	p := mem.Profile
	all := memory.Render(p)
	// Facts and quirks must not take Sara's claim; a boundary about it is fine.
	claimTaken := strings.Contains(itemsText(p.Facts)+itemsText(p.Quirks)+p.Summary, "تنبل")
	ok := !mem.ProfileUpdatedAt.IsZero() && persian(all) && strings.Contains(all, "پرسپولیس") && !claimTaken
	return ok, strings.ReplaceAll(all, "\n", " | ")
}

// mentionsOldTopic reports whether s brings back the 2-hour-old café plan.
func mentionsOldTopic(s string) bool {
	return strings.Contains(s, "کافه") || strings.Contains(s, "قهوه") || strings.Contains(s, "ساعت ۵")
}

// mentionsQuirk reports whether s reaches for a remembered quirk or fact that
// has nothing to do with the question.
func mentionsQuirk(s string) bool {
	for _, w := range []string{"دیر", "رژیم", "پرسپولیس", "مهندس", "طراح", "گربه"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// heightJoke reports whether s mentions Ali's height, which his boundaries rule out.
func heightJoke(s string) bool {
	for _, w := range []string{"قدش", "قدت", "قد کوتاه", "قدبلند", "قد بلند", "کوتاه‌قد", "کوتوله"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func itemsText(items []store.Item) string {
	var parts []string
	for _, it := range items {
		parts = append(parts, it.Text)
	}
	return strings.Join(parts, "؛ ")
}

// chatScene stores a short group conversation with profiles.
func chatScene(b *Bot, db *store.DB) {
	profiles(db)
	say(b, 1, sara, "بچه‌ها فردا ساعت ۵ کافه همیشگی؟")
	say(b, 2, ali, "من هستم ولی شاید یکم دیر برسم 😅")
	say(b, 3, sara, "علی تو همیشه دیر میرسی 😂")
}

// profiles stores the members' memory.
func profiles(db *store.DB) {
	_, _ = db.UpdateMember(testChat, ali.ID, func(m *store.Member) {
		m.Profile.Nickname = "علی فوتبالی"
		m.Profile.Facts = []store.Item{{Text: "طرفدار سرسخت پرسپولیس"}, {Text: "دانشجوی مهندسی"}}
		m.Profile.Quirks = []store.Item{{Text: "همیشه دیر می‌رسد"}, {Text: "قول رژیم می‌دهد و نمی‌گیرد"}}
		m.Profile.Boundaries = []store.Item{{Text: "شوخی با قدش را دوست ندارد"}}
	})
	_, _ = db.UpdateMember(testChat, sara.ID, func(m *store.Member) {
		m.Profile.Facts = []store.Item{{Text: "طراح گرافیک"}}
		m.Profile.Interests = []store.Item{{Text: "گربه‌ها"}, {Text: "قهوه"}}
	})
}

// persian reports whether most letters of s are Persian/Arabic script.
func persian(s string) bool {
	n, fa := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
			if unicode.Is(unicode.Arabic, r) {
				fa++
			}
		}
	}
	return n > 0 && fa*10 >= n*8
}

type result struct {
	passed  int
	runs    int
	total   time.Duration
	samples []string
	runes   int // length of the bot's chat replies (faal poems excluded)
	replies int
}

// benchModel is the model under test and the llm.* settings it runs with.
type benchModel struct {
	Model       string
	Temperature float64
	MaxTokens   int
	Timeout     time.Duration
	Tools       bool
	JSONSchema  bool
}

func TestLiveBench(t *testing.T) {
	base := os.Getenv("LMSTUDIO_BASE_URL")
	if base == "" {
		t.Skip("LMSTUDIO_BASE_URL not set")
	}
	runs := 3
	fmt.Sscan(envOr("BENCH_RUNS", "3"), &runs)
	only := os.Getenv("BENCH_ONLY") // comma-separated scenario names

	// The llm.* and bot.* settings come from config.example.yaml.
	os.Setenv("HOOSHANG_TELEGRAM_TOKEN", "bench")
	exampleCfg, err := config.Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	models := strings.Split(envOr("BENCH_MODELS", exampleCfg.LLM.Model), ",")

	results := map[string]map[string]*result{}
	for _, model := range models {
		l := exampleCfg.LLM
		mc := benchModel{Model: model, Temperature: l.Temperature, MaxTokens: l.MaxTokens, Timeout: l.Timeout, Tools: l.Tools, JSONSchema: l.JSONSchema}
		results[model] = map[string]*result{}
		client := llm.New(base, "lm-studio", mc.Timeout)
		// Warm-up: loads the model in LM Studio so load time isn't measured.
		_, _ = client.Chat(context.Background(), llm.Request{Model: model, Messages: []llm.Message{{Role: "user", Content: "سلام"}}, MaxTokens: mc.MaxTokens})

		for _, sc := range scenarios {
			if only != "" && !strings.Contains(","+only+",", ","+sc.name+",") {
				continue
			}
			r := &result{}
			results[model][sc.name] = r
			for i := 0; i < runs; i++ {
				b, db, tg, _ := replyBot(t, 100)
				b.deps.LLM = client
				b.deps.Cfg.LLM.Model = model
				b.deps.Cfg.LLM.Temperature, b.deps.Cfg.LLM.MaxTokens, b.deps.Cfg.LLM.Tools = mc.Temperature, mc.MaxTokens, mc.Tools
				b.deps.Cfg.Bot.HistoryMessages = exampleCfg.Bot.HistoryMessages
				b.deps.Cfg.Bot.HistoryWindow = exampleCfg.Bot.HistoryWindow
				b.deps.Cfg.Bot.ProfileBudget, b.deps.Cfg.Bot.RandomProfiles = exampleCfg.Bot.ProfileBudget, exampleCfg.Bot.RandomProfiles
				start := time.Now()
				ok, out := sc.run(t, b, db, tg, mc)
				for _, txt := range tg.sentTexts() { // leaked model markup fails any scenario
					if strings.Contains(txt, "<|") || strings.Contains(txt, "|>") {
						ok, out = false, "LEAKED MARKUP: "+out
					}
				}
				d := time.Since(start)
				r.runs++
				r.total += d
				mark := "✗"
				if ok {
					r.passed++
					mark = "✓"
				}
				for _, s := range tg.sentTexts() {
					if !strings.HasPrefix(s, i18n.FaalHeader) {
						r.runes += len([]rune(s))
						r.replies++
					}
				}
				r.samples = append(r.samples, fmt.Sprintf("%s %.0fs — %s", mark, d.Seconds(), out))
				t.Logf("[%s] %s run %d: %s %.1fs\n    %s", model, sc.name, i+1, mark, d.Seconds(), out)
			}
		}
	}

	report := renderReport(models, results, runs)
	t.Log("\n" + report)
	if path := os.Getenv("BENCH_OUT"); path != "" {
		if !filepath.IsAbs(path) {
			path = filepath.Join("..", "..", path) // go test runs in internal/bot
		}
		if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func renderReport(models []string, results map[string]map[string]*result, runs int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Benchmark %s, %d runs per scenario.\n\n", time.Now().Format("2006-01-02 15:04"), runs)
	sb.WriteString("| Scenario |")
	for _, m := range models {
		fmt.Fprintf(&sb, " %s |", m)
	}
	sb.WriteString("\n|---|")
	for range models {
		sb.WriteString("---|")
	}
	sb.WriteString("\n")
	var names []string
	for _, sc := range scenarios {
		names = append(names, sc.name)
	}
	for _, name := range names {
		row := fmt.Sprintf("| %s |", name)
		seen := false
		for _, m := range models {
			r := results[m][name]
			if r == nil {
				row += " – |"
				continue
			}
			seen = true
			length := ""
			if r.replies > 0 {
				length = fmt.Sprintf(", %d chars", r.runes/r.replies)
			}
			row += fmt.Sprintf(" %d/%d, avg %.0fs%s |", r.passed, r.runs, r.total.Seconds()/float64(r.runs), length)
		}
		if seen {
			sb.WriteString(row + "\n")
		}
	}
	for _, m := range models {
		fmt.Fprintf(&sb, "\n### %s\n\n", m)
		keys := make([]string, 0, len(results[m]))
		for k := range results[m] {
			keys = append(keys, k)
		}
		sort.SliceStable(keys, func(i, j int) bool { return indexOf(names, keys[i]) < indexOf(names, keys[j]) })
		for _, k := range keys {
			fmt.Fprintf(&sb, "**%s**\n\n", k)
			for _, s := range results[m][k].samples {
				fmt.Fprintf(&sb, "- %s\n", s)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return len(xs)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

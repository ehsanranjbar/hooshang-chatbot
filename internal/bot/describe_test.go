package bot

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func enableMedia(b *Bot) {
	b.deps.Cfg.Features.Media = &config.MediaFeature{Enabled: true, Photos: true, Stickers: true, GIFs: true, Send: true,
		Words: 30, CacheTTL: 24 * time.Hour, MaxQueue: 5, Frames: 1}
}

// receive stores an incoming message the way handleMessage does, without
// queuing replies or spontaneous messages.
func receive(b *Bot, m *telego.Message) {
	text, _ := messageText(m)
	b.storeIncoming(m, text)
	b.describeIncoming(m)
}

func mediaMsg(id int, from *telego.User, set func(*telego.Message)) *telego.Message {
	m := &telego.Message{MessageID: id, Chat: telego.Chat{ID: testChat, Type: "supergroup"}, From: from, Date: time.Now().Unix()}
	set(m)
	return m
}

func TestStickerDescriptionIsCachedAndReused(t *testing.T) {
	webp, err := os.ReadFile("testdata/sticker.webp")
	if err != nil {
		t.Fatal(err)
	}
	b, db, tg, fake := replyBot(t, 10, llm.Response{Content: "«گربه‌ای که از خنده غش کرده»"})
	enableMedia(b)
	tg.serveFiles(t, map[string][]byte{"stk": webp})
	sticker := &telego.Sticker{FileID: "stk", FileUniqueID: "u-stk", Emoji: "😂"}

	m := mediaMsg(1, sara, func(m *telego.Message) { m.Sticker = sticker })
	receive(b, m)
	b.describeTrigger(context.Background(), m) // no background worker in tests
	got, _ := db.GetMessage(testChat, 1)
	if got.Text != "[استیکر: 😂 — گربه‌ای که از خنده غش کرده]" || !got.Described {
		t.Fatalf("stored = %q", got.Text)
	}
	if len(fake.Calls) != 1 || len(fake.Calls[0].Messages[0].Images) != 1 || fake.Calls[0].Messages[0].Images[0].MIME != "image/png" {
		t.Fatalf("the describe call did not send the sticker as PNG: %+v", fake.Calls)
	}
	if p := fake.Calls[0].Messages[0].Content; !strings.Contains(p, "30 words") || !strings.Contains(p, "reaction") {
		t.Errorf("describe prompt: %s", p)
	}

	// The same sticker again, in a message of its own: from the cache, no download, no model call.
	m2 := mediaMsg(2, ali, func(m *telego.Message) { m.Sticker = sticker })
	receive(b, m2)
	got, _ = db.GetMessage(testChat, 2)
	if !strings.Contains(got.Text, "گربه‌ای که از خنده") || len(fake.Calls) != 1 || tg.getFiles != 1 {
		t.Fatalf("not reused from the cache: %q, calls %d, downloads %d", got.Text, len(fake.Calls), tg.getFiles)
	}
}

func TestPhotoIsDescribedBeforeTheReply(t *testing.T) {
	webp, _ := os.ReadFile("testdata/sticker.webp")
	b, _, tg, fake := replyBot(t, 10,
		llm.Response{Content: "یک ساحل شنی در غروب با دو صندلی"},
		llm.Response{Content: "به به، کجاست اینجا؟"},
	)
	enableMedia(b)
	tg.serveFiles(t, map[string][]byte{"big": webp})
	m := mediaMsg(1, ali, func(m *telego.Message) {
		m.Photo = []telego.PhotoSize{{FileID: "small", Width: 90, Height: 60}, {FileID: "big", FileUniqueID: "u-big", Width: 800, Height: 600}}
		m.Caption = "هوشنگ اینجا رو ببین"
	})
	receive(b, m)
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if len(fake.Calls) != 2 {
		t.Fatalf("calls = %d", len(fake.Calls))
	}
	if p := fake.Calls[1].Messages[1].Content; !strings.Contains(p, "[عکس: یک ساحل شنی در غروب با دو صندلی] هوشنگ اینجا رو ببین") {
		t.Errorf("the reply prompt has no description:\n%s", p)
	}
	if len(fake.Calls[1].Messages[1].Images) != 1 || tg.getFiles != 1 {
		t.Errorf("the picture should be attached from the single download: images %d, downloads %d", len(fake.Calls[1].Messages[1].Images), tg.getFiles)
	}
	if got := tg.sentTexts(); len(got) != 1 {
		t.Fatalf("sent = %q", got)
	}
}

func TestMediaRefs(t *testing.T) {
	b, _, _, _ := replyBot(t, 10)
	enableMedia(b)
	thumb := &telego.PhotoSize{FileID: "thumb"}
	ref, ok := b.mediaRefOf(&telego.Message{Sticker: &telego.Sticker{FileID: "tgs", FileUniqueID: "u", IsAnimated: true, Thumbnail: thumb}})
	if !ok || ref.fileID != "thumb" || ref.uniqueID != "u" || !ref.cache {
		t.Errorf("animated sticker ref = %+v", ref)
	}
	ref, ok = b.mediaRefOf(&telego.Message{Animation: &telego.Animation{FileID: "mp4", FileUniqueID: "g", Thumbnail: thumb}})
	if !ok || ref.fileID != "thumb" || ref.uniqueID != "g" {
		t.Errorf("GIF ref = %+v", ref)
	}
	b.deps.Cfg.Features.Media.GIFs = false
	if _, ok := b.mediaRefOf(&telego.Message{Animation: &telego.Animation{Thumbnail: thumb}}); ok {
		t.Error("GIFs are off but got a ref")
	}
}

func TestDescribedTagAndClean(t *testing.T) {
	if got := describedTag("[عکس]", "ساحل"); got != "[عکس: ساحل]" {
		t.Errorf("photo tag = %q", got)
	}
	if got := describedTag("[گیف]", "خنده"); got != "[گیف: خنده]" {
		t.Errorf("gif tag = %q", got)
	}
	long := strings.Repeat("کلمه ", 60)
	if got := cleanDescription(long, 30); len(strings.Fields(got)) != 40 || !strings.HasSuffix(got, "…") {
		t.Errorf("not capped: %d words", len(strings.Fields(got)))
	}
	if got := cleanDescription("یک [متن] عجیب\nدو خط", 30); got != "یک (متن) عجیب دو خط" {
		t.Errorf("clean = %q", got)
	}
}

func TestTriggerWaitsForAnInFlightDescription(t *testing.T) {
	b, db, _, fake := replyBot(t, 10)
	enableMedia(b)
	b.describer = newDescriber(b)
	sticker := &telego.Sticker{FileID: "stk", FileUniqueID: "u", Emoji: "😂"}
	m := mediaMsg(1, sara, func(m *telego.Message) { m.Sticker = sticker })
	receive(b, m) // queued, not run: no worker goroutine
	if _, ok := b.describer.claim("stk"); !ok {
		t.Fatal("claim failed")
	}
	done := make(chan struct{})
	go func() { b.describeTrigger(context.Background(), m); close(done) }()
	select {
	case <-done:
		t.Fatal("describeTrigger did not wait for the in-flight description")
	case <-time.After(50 * time.Millisecond):
	}
	b.applyDescription(describeJob{chatID: testChat, msgID: 1, tag: mediaTag(m)}, "خنده")
	b.describer.release("stk")
	<-done
	if got, _ := db.GetMessage(testChat, 1); got.Text != "[استیکر: 😂 — خنده]" || len(fake.Calls) != 0 {
		t.Fatalf("stored %q, model calls %d", got.Text, len(fake.Calls))
	}
}

func TestReplyToAPictureAttachesIt(t *testing.T) {
	webp, _ := os.ReadFile("testdata/sticker.webp")
	b, _, tg, fake := replyBot(t, 10,
		llm.Response{Content: "یه مرد با کلاه"}, // the description of the photo
		llm.Response{Content: "سه نفر"},
	)
	enableMedia(b)
	tg.serveFiles(t, map[string][]byte{"p": webp})
	photo := mediaMsg(1, sara, func(m *telego.Message) {
		m.Photo = []telego.PhotoSize{{FileID: "p", FileUniqueID: "u-p", Width: 800, Height: 600}}
	})
	receive(b, photo)
	b.describeTrigger(context.Background(), photo)

	q := say(b, 2, ali, "هوشنگ تو این عکس چند نفرن؟")
	q.ReplyToMessage = photo
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: q})
	if len(fake.Calls) != 2 {
		t.Fatalf("calls = %d", len(fake.Calls))
	}
	user := fake.Calls[1].Messages[1]
	if len(user.Images) != 1 || !strings.Contains(user.Content, "The picture of message [1] is attached") {
		t.Fatalf("the picture was not attached to the reply: images %d\n%s", len(user.Images), user.Content)
	}

	// The bot's own cards are not re-sent.
	card := mediaMsg(3, &telego.User{ID: testBotID, IsBot: true}, func(m *telego.Message) {
		m.Photo = []telego.PhotoSize{{FileID: "p", Width: 800, Height: 600}}
	})
	if _, _, ok := b.questionImage(context.Background(), &telego.Message{Chat: q.Chat, ReplyToMessage: card}, nil); ok {
		t.Error("the bot's own image was attached")
	}
}

func TestFindAndSendMedia(t *testing.T) {
	webp, _ := os.ReadFile("testdata/sticker.webp")
	b, db, tg, _ := replyBot(t, 10, llm.Response{Content: "گربه‌ای که از خنده غش کرده، برای خنده‌ی زیاد"})
	enableMedia(b)
	tg.serveFiles(t, map[string][]byte{"stk": webp, "thumb": webp})
	m := mediaMsg(1, sara, func(m *telego.Message) {
		m.Sticker = &telego.Sticker{FileID: "stk", FileUniqueID: "u-stk", Emoji: "😂"}
	})
	receive(b, m)
	b.describeTrigger(context.Background(), m)
	_ = db.PutMediaDesc(store.MediaDesc{Text: "مردی که چشم‌هایش را می‌چرخاند", UniqueID: "u-gif", Kind: "gif", FileID: "gif1", Uses: 5}, time.Hour)

	// The same sticker sent again counts as a use.
	receive(b, mediaMsg(2, ali, func(m *telego.Message) {
		m.Sticker = &telego.Sticker{FileID: "stk2", FileUniqueID: "u-stk", Emoji: "😂"}
	}))
	if c, _ := db.GetMediaDesc("u-stk"); c.Uses != 2 || c.FileID != "stk2" || c.Kind != "sticker" {
		t.Fatalf("cache entry = %+v", c)
	}

	q := say(b, 3, ali, "هوشنگ یه استیکر خنده بفرست")
	j := job{kind: jobReply, chatID: testChat, msg: q}
	out, _ := b.toolFindMedia(j)(context.Background(), `{"query":"😂"}`)
	if !strings.HasPrefix(out, "u-stk: [sticker 😂]") || strings.Contains(out, "u-gif") {
		t.Fatalf("emoji search = %q", out)
	}
	if out, _ = b.toolFindMedia(j)(context.Background(), `{"query":"چشم","kind":"gif"}`); !strings.HasPrefix(out, "u-gif:") {
		t.Fatalf("word search = %q", out)
	}
	if out, _ = b.toolSendMedia(j)(context.Background(), `{"id":"u-stk"}`); !strings.HasPrefix(out, "sent sticker") {
		t.Fatalf("send = %q", out)
	}
	if len(tg.stickers) != 1 || tg.stickers[0].Sticker.FileID != "stk2" || tg.stickers[0].ReplyParameters.MessageID != 3 {
		t.Fatalf("stickers sent = %+v", tg.stickers)
	}
	tr := b.renderTranscript(job{kind: jobReply, chatID: testChat, msg: say(b, 9, sara, "هوشنگ؟")})
	if !strings.Contains(tr, "(you): [استیکر: 😂 — گربه‌ای که از خنده غش کرده") {
		t.Errorf("the sent sticker is not in the transcript:\n%s", tr)
	}
	if out, _ = b.toolSendMedia(j)(context.Background(), `{"id":"nope"}`); !strings.HasPrefix(out, "error") {
		t.Errorf("unknown id = %q", out)
	}
}

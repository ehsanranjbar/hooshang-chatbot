package bot

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func TestProfileSeedFromTelegram(t *testing.T) {
	webp, err := os.ReadFile("testdata/sticker.webp")
	if err != nil {
		t.Fatal(err)
	}
	b, db, tg, fake := replyBot(t, 10, llm.Response{Content: "مردی با کلاه آبی که به دوربین می‌خندد"})
	enableMedia(b)
	b.describer = newDescriber(b) // seeds run on the describer's queue; tests run them by hand
	tg.serveFiles(t, map[string][]byte{"pp1": webp})
	tg.profilePhotos = &telego.UserProfilePhotos{TotalCount: 1, Photos: [][]telego.PhotoSize{{{FileID: "pp1", FileUniqueID: "u1", Width: 640, Height: 640}}}}
	tg.chatInfo = &telego.ChatFullInfo{ID: ali.ID, Bio: "برنامه‌نویس و عاشق قهوه", Birthdate: &telego.Birthdate{Day: 5, Month: 11}}
	p := b.deps.Cfg.Features.Profiles
	p.Seed.Photos, p.Seed.Bio, p.Seed.Refresh = 2, true, 30*24*time.Hour

	// A first message from Ali queues the seed.
	say(b, 1, ali, "سلام")
	if len(b.describer.queue) != 1 {
		t.Fatalf("seed not queued: %d jobs", len(b.describer.queue))
	}
	j := <-b.describer.queue
	j.fn(context.Background())

	mem, _ := db.GetMemory(testChat, ali.ID)
	looks, facts := mem.Kind(store.KindLook), mem.Kind(store.KindFact)
	if len(looks) != 1 || !strings.Contains(looks[0].Text, "کلاه آبی") || looks[0].Expires.IsZero() {
		t.Fatalf("looks = %+v", looks)
	}
	var texts []string
	for _, f := range facts {
		texts = append(texts, f.Text)
	}
	joined := strings.Join(texts, " | ")
	if !strings.Contains(joined, "بیو: برنامه‌نویس و عاشق قهوه") || !strings.Contains(joined, "تولد: ۱۴ آبان") {
		t.Fatalf("facts = %s", joined)
	}
	if len(fake.Calls) != 1 || len(fake.Calls[0].Messages[0].Images) != 1 || !strings.Contains(fake.Calls[0].Messages[0].Content, "profile picture") {
		t.Errorf("describe call: %+v", fake.Calls)
	}
	m, _ := db.GetMember(testChat, ali.ID)
	if m.SeededAt.IsZero() {
		t.Error("SeededAt not set")
	}
	// The next message does not seed again (refresh is 30 days).
	say(b, 2, ali, "دوباره")
	if len(b.describer.queue) != 0 {
		t.Error("seeded again before the refresh")
	}
	// Profiles off: nothing is queued.
	b.deps.Cfg.Features.Profiles = nil
	say(b, 3, sara, "سلام")
	if len(b.describer.queue) != 0 {
		t.Error("seeded with profiles off")
	}
}

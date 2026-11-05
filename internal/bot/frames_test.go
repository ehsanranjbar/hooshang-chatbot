package bot

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/llm"
)

// needFFmpeg skips the test when ffmpeg or ffprobe is not installed.
func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not in PATH", n)
		}
	}
}

// clipFixture returns the test clip and the thumbnail served for it.
func clipFixture(t *testing.T) (clip, thumb []byte) {
	t.Helper()
	clip, err := os.ReadFile("testdata/clip.webm")
	if err != nil {
		t.Fatal(err)
	}
	thumb, err = os.ReadFile("testdata/sticker.webp")
	if err != nil {
		t.Fatal(err)
	}
	return clip, thumb
}

func TestExtractFrames(t *testing.T) {
	needFFmpeg(t)
	clip, _ := clipFixture(t)
	ffmpeg, _ := exec.LookPath("ffmpeg")
	ffprobe, _ := exec.LookPath("ffprobe")
	for _, n := range []int{2, 3, 5} {
		frames, err := extractFrames(context.Background(), ffmpeg, ffprobe, clip, n)
		if err != nil || len(frames) != n {
			t.Fatalf("n=%d: %d frames, err %v", n, len(frames), err)
		}
		var prev []byte
		for i, f := range frames {
			cfg, err := png.DecodeConfig(bytes.NewReader(f.Data))
			if err != nil || f.MIME != "image/png" || max(cfg.Width, cfg.Height) > 512 {
				t.Fatalf("n=%d frame %d: %+v, err %v", n, i, cfg, err)
			}
			if bytes.Equal(f.Data, prev) {
				t.Errorf("n=%d: frame %d is a copy of the one before", n, i)
			}
			prev = f.Data
		}
	}
	if _, err := extractFrames(context.Background(), ffmpeg, ffprobe, []byte("not a video"), 3); err == nil {
		t.Error("garbage gave frames")
	}
}

func TestVideoStickerIsDescribedFromFrames(t *testing.T) {
	needFFmpeg(t)
	clip, thumb := clipFixture(t)
	b, db, tg, fake := replyBot(t, 10, llm.Response{Content: "گربه‌ای که می‌رقصد"})
	enableMedia(b)
	b.deps.Cfg.Media.Frames = 3
	tg.serveFiles(t, map[string][]byte{"clip": clip, "thumb": thumb})
	sticker := &telego.Sticker{FileID: "clip", FileUniqueID: "u-clip", Emoji: "😂", IsVideo: true, Thumbnail: &telego.PhotoSize{FileID: "thumb"}}
	m := mediaMsg(1, sara, func(m *telego.Message) { m.Sticker = sticker })
	receive(b, m)
	b.describeTrigger(context.Background(), m)

	got, _ := db.GetMessage(testChat, 1)
	if got.Text != "[استیکر: 😂 — گربه‌ای که می‌رقصد]" || !got.Described {
		t.Fatalf("stored = %q", got.Text)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %d", len(fake.Calls))
	}
	msg := fake.Calls[0].Messages[0]
	if len(msg.Images) != 3 {
		t.Fatalf("images = %d, want 3", len(msg.Images))
	}
	for _, img := range msg.Images {
		if img.MIME != "image/png" {
			t.Errorf("image MIME = %s", img.MIME)
		}
	}
	if !strings.Contains(msg.Content, "3 frames") || !strings.Contains(msg.Content, "animated sticker") || strings.Contains(msg.Content, "thumbnail") {
		t.Errorf("prompt:\n%s", msg.Content)
	}
	if c, err := db.GetMediaDesc("u-clip"); err != nil || c.Text != "گربه‌ای که می‌رقصد" || c.FileID != "clip" {
		t.Errorf("cache entry = %+v, err %v", c, err)
	}
}

func TestMissingFFmpegFallsBackToTheThumbnail(t *testing.T) {
	clip, thumb := clipFixture(t)
	b, db, tg, fake := replyBot(t, 10, llm.Response{Content: "گربه‌ای که می‌رقصد"})
	enableMedia(b)
	b.deps.Cfg.Media.Frames = 3
	b.deps.Cfg.Media.FFmpeg = "/nonexistent/ffmpeg"
	tg.serveFiles(t, map[string][]byte{"clip": clip, "thumb": thumb})
	sticker := &telego.Sticker{FileID: "clip", FileUniqueID: "u-clip", Emoji: "😂", IsVideo: true, Thumbnail: &telego.PhotoSize{FileID: "thumb"}}
	m := mediaMsg(1, sara, func(m *telego.Message) { m.Sticker = sticker })
	receive(b, m)
	b.describeTrigger(context.Background(), m)

	got, _ := db.GetMessage(testChat, 1)
	if !got.Described || !strings.Contains(got.Text, "گربه‌ای که می‌رقصد") {
		t.Fatalf("stored = %q", got.Text)
	}
	msg := fake.Calls[0].Messages[0]
	if len(msg.Images) != 1 || !strings.Contains(msg.Content, "animated sticker (its thumbnail)") || strings.Contains(msg.Content, "frames") {
		t.Errorf("images %d, prompt:\n%s", len(msg.Images), msg.Content)
	}
	if tg.getFiles != 1 { // the clip is not downloaded when there is no ffmpeg
		t.Errorf("downloads = %d, want 1 (the thumbnail)", tg.getFiles)
	}
}

func TestGIFWithOneFrameUsesTheThumbnail(t *testing.T) {
	clip, thumb := clipFixture(t)
	b, _, tg, fake := replyBot(t, 10, llm.Response{Content: "مردی که چشم می‌چرخاند"})
	enableMedia(b)
	b.deps.Cfg.Media.Frames = 1
	tg.serveFiles(t, map[string][]byte{"gif": clip, "thumb": thumb})
	m := mediaMsg(1, sara, func(m *telego.Message) {
		m.Animation = &telego.Animation{FileID: "gif", FileUniqueID: "u-gif", Thumbnail: &telego.PhotoSize{FileID: "thumb"}}
	})
	receive(b, m)
	b.describeTrigger(context.Background(), m)
	msg := fake.Calls[0].Messages[0]
	if len(msg.Images) != 1 || !strings.Contains(msg.Content, "GIF (its first frame)") || tg.getFiles != 1 {
		t.Errorf("images %d, downloads %d, prompt:\n%s", len(msg.Images), tg.getFiles, msg.Content)
	}
	if b.ffmpeg != "" { // never resolved: no ffmpeg run was even attempted
		t.Error("ffmpeg was looked up")
	}
}

func TestAnimatedTGSStickerStaysThumbnailOnly(t *testing.T) {
	b, _, _, _ := replyBot(t, 10)
	enableMedia(b)
	ref, ok := b.mediaRefOf(&telego.Message{Sticker: &telego.Sticker{FileID: "tgs", FileUniqueID: "u", IsAnimated: true, Thumbnail: &telego.PhotoSize{FileID: "th"}}})
	if !ok || ref.clipID != "" || ref.fileID != "th" || ref.kind != "animated sticker (its thumbnail)" {
		t.Errorf("TGS ref = %+v", ref)
	}
	ref, _ = b.mediaRefOf(&telego.Message{Sticker: &telego.Sticker{FileID: "webm", FileUniqueID: "u", IsVideo: true, Thumbnail: &telego.PhotoSize{FileID: "th"}}})
	if ref.clipID != "webm" || ref.fileID != "th" {
		t.Errorf("video sticker ref = %+v", ref)
	}
}

func TestReplyAboutAGIFAttachesTheFrames(t *testing.T) {
	needFFmpeg(t)
	clip, thumb := clipFixture(t)
	b, _, tg, fake := replyBot(t, 10,
		llm.Response{Content: "مردی که چشم می‌چرخاند"}, // the description of the GIF
		llm.Response{Content: "اول می‌خنده بعد گریه می‌کنه"},
	)
	enableMedia(b)
	b.deps.Cfg.Media.Frames = 3
	tg.serveFiles(t, map[string][]byte{"gif": clip, "thumb": thumb})
	gif := mediaMsg(1, sara, func(m *telego.Message) {
		m.Animation = &telego.Animation{FileID: "gif", FileUniqueID: "u-gif", Thumbnail: &telego.PhotoSize{FileID: "thumb"}}
	})
	receive(b, gif)
	b.describeTrigger(context.Background(), gif)
	if len(fake.Calls) != 1 || len(fake.Calls[0].Messages[0].Images) != 3 || !strings.Contains(fake.Calls[0].Messages[0].Content, "GIF") {
		t.Fatalf("the GIF was not described from frames: %+v", fake.Calls)
	}

	// A question about it: the frames come back (it is already described, so no second description).
	q := say(b, 2, ali, "هوشنگ تو این گیف چی میشه؟")
	q.ReplyToMessage = gif
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: q})
	if len(fake.Calls) != 2 {
		t.Fatalf("calls = %d", len(fake.Calls))
	}
	user := fake.Calls[1].Messages[1]
	if len(user.Images) != 3 || !strings.Contains(user.Content, "3 pictures attached are frames") || !strings.Contains(user.Content, "message [1]") {
		t.Fatalf("frames not attached: images %d\n%s", len(user.Images), user.Content)
	}

	// A message that is itself the GIF reuses the frames describeTrigger took.
	own := mediaMsg(3, ali, func(m *telego.Message) {
		m.Animation = &telego.Animation{FileID: "gif", FileUniqueID: "u-gif2", Thumbnail: &telego.PhotoSize{FileID: "thumb"}}
	})
	pre := []llm.Image{{MIME: "image/png", Data: []byte("a")}, {MIME: "image/png", Data: []byte("b")}}
	before := tg.getFiles
	if imgs, id, ok := b.questionImage(context.Background(), own, pre); !ok || id != 3 || len(imgs) != 2 || tg.getFiles != before {
		t.Errorf("pre not reused: %d images, id %d, downloads %d→%d", len(imgs), id, before, tg.getFiles)
	}
}

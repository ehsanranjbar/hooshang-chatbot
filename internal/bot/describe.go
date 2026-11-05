package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // decoders for image.Decode
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	"golang.org/x/image/webp"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// mediaRef is a describable picture of a message: the photo, the sticker (or
// its thumbnail when it is animated) or a GIF's thumbnail, plus the clip
// itself when frames can be taken from it.
type mediaRef struct {
	kind     string // for the prompt: "photo", "sticker", "GIF (its first frame)", "animated sticker (its thumbnail)"
	framed   string // the kind when frames of the clip are sent: "animated sticker" or "GIF"
	fileID   string // what to download (the thumbnail for clips)
	clipID   string // the video sticker or GIF to take frames from ("" = none: TGS, photos, static stickers)
	uniqueID string // the cache key: the sticker's or GIF's file_unique_id
	cache    bool   // stickers and GIFs repeat; photos don't
	hint     string // the sticker's emoji or the caption
	media    string // the cache kind: "sticker" or "gif" ("" for photos)
	sendID   string // the file to send it again (the sticker or GIF itself, not its thumbnail)
	emoji    string // the sticker's emoji
}

// minPhotoSide is the smallest photo side worth sending to the model.
const minPhotoSide = 512

// mediaRefOf returns the message's describable picture, if media description
// is on for its kind.
func (b *Bot) mediaRefOf(m *telego.Message) (mediaRef, bool) {
	c := b.deps.Cfg.Features.Media
	if !c.On() {
		return mediaRef{}, false
	}
	switch {
	case len(m.Photo) > 0 && c.Photos:
		best := m.Photo[len(m.Photo)-1] // sizes come smallest first
		for _, p := range m.Photo {
			if max(p.Width, p.Height) >= minPhotoSide {
				best = p
				break
			}
		}
		return mediaRef{kind: "photo", fileID: best.FileID, uniqueID: best.FileUniqueID, hint: m.Caption}, best.FileID != ""
	case m.Sticker != nil && c.Stickers:
		s := m.Sticker
		r := mediaRef{kind: "sticker", fileID: s.FileID, uniqueID: s.FileUniqueID, cache: true, hint: s.Emoji,
			media: "sticker", sendID: s.FileID, emoji: s.Emoji}
		if s.IsAnimated || s.IsVideo { // TGS / WEBM: the thumbnail is the only still picture
			if s.Thumbnail == nil {
				return mediaRef{}, false
			}
			r.kind, r.fileID = "animated sticker (its thumbnail)", s.Thumbnail.FileID
			if s.IsVideo { // ffmpeg can decode WEBM, not Lottie (TGS)
				r.clipID, r.framed = s.FileID, "animated sticker"
			}
		}
		return r, true
	case m.Animation != nil && c.GIFs && m.Animation.Thumbnail != nil:
		a := m.Animation
		return mediaRef{kind: "GIF (its first frame)", framed: "GIF", fileID: a.Thumbnail.FileID, clipID: a.FileID,
			uniqueID: a.FileUniqueID, cache: true, hint: m.Caption, media: "gif", sendID: a.FileID}, true
	}
	return mediaRef{}, false
}

// describeJob is one pending description.
type describeJob struct {
	chatID, msgID int64
	tag           string // the stored media tag the description goes into
	ref           mediaRef
}

// newDescriber returns the background queue of media descriptions.
func newDescriber(b *Bot) *enricher {
	return newEnricher(b.deps.Cfg.Features.Media.MaxQueue)
}

// describeIncoming adds a description to a stored media message: from the
// cache right away, else in the background (skipped when the queue is full).
func (b *Bot) describeIncoming(m *telego.Message) {
	ref, ok := b.mediaRefOf(m)
	if !ok {
		return
	}
	j := describeJob{chatID: m.Chat.ID, msgID: int64(m.MessageID), tag: mediaTag(m), ref: ref}
	if ref.cache {
		if c, err := b.deps.DB.GetMediaDesc(ref.uniqueID); err == nil {
			b.applyDescription(j, c.Text)
			_ = b.deps.DB.SeenMedia(ref.uniqueID, ref.sendID, ref.emoji, b.deps.Cfg.Features.Media.CacheTTL)
			return
		}
	}
	if b.describer == nil {
		return
	}
	queued := b.describer.submit(ref.fileID, func(ctx context.Context) {
		if _, err := b.describe(ctx, j); err != nil {
			b.logger.Warn("media description failed", "chat", j.chatID, "kind", j.ref.kind, "err", err)
		}
	})
	if !queued {
		b.logger.Debug("media description skipped: queue full", "chat", j.chatID)
	}
}

// describeTrigger describes the media of a message the bot is about to answer,
// if it has none yet, so the reply can see it. It returns the images it
// downloaded, if any, so the reply needn't download them again.
func (b *Bot) describeTrigger(ctx context.Context, m *telego.Message) []llm.Image {
	if m == nil {
		return nil
	}
	ref, ok := b.mediaRefOf(m)
	if !ok {
		return nil
	}
	if s, err := b.deps.DB.GetMessage(m.Chat.ID, int64(m.MessageID)); err != nil || s.Described {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, b.deps.Cfg.LLM.Timeout)
	defer cancel()
	if d := b.describer; d != nil {
		wait, ok := d.claim(ref.fileID)
		if !ok { // the background worker has it: wait instead of describing it twice
			select {
			case <-wait:
			case <-ctx.Done():
			}
			return nil
		}
		defer d.release(ref.fileID)
	}
	j := describeJob{chatID: m.Chat.ID, msgID: int64(m.MessageID), tag: mediaTag(m), ref: ref}
	imgs, err := b.describe(ctx, j)
	if err != nil {
		b.logger.Warn("media description failed", "chat", j.chatID, "kind", ref.kind, "err", err)
	}
	return imgs
}

// describe describes the media of a job (from the cache when it can) and
// writes the description into the stored message. It returns the images when
// it had to download them: the frames of an animated sticker or GIF, else the
// one picture.
func (b *Bot) describe(ctx context.Context, j describeJob) ([]llm.Image, error) {
	if j.ref.cache {
		if c, err := b.deps.DB.GetMediaDesc(j.ref.uniqueID); err == nil {
			b.applyDescription(j, c.Text)
			return nil, nil
		}
	}
	imgs, framed, err := b.mediaImages(ctx, j.ref)
	if err != nil {
		return nil, err
	}
	c := b.deps.Cfg.Features.Media
	kind, frames := j.ref.kind, 1
	if framed {
		kind, frames = j.ref.framed, len(imgs)
	}
	task, _ := prompts.MediaDescribe(prompts.MediaData{
		Language: b.deps.Cfg.Bot.Language, Kind: kind, Hint: j.ref.hint,
		Reaction: j.ref.kind != "photo", Words: c.Words, Frames: frames,
	})
	resp, err := b.deps.LLM.Chat(ctx, llm.Request{
		Model:       b.deps.Cfg.LLM.Model,
		Messages:    []llm.Message{{Role: "user", Content: task, Images: imgs}},
		Temperature: 0.2,
		MaxTokens:   b.deps.Cfg.LLM.MaxTokens,
	})
	if err != nil {
		return imgs, err
	}
	desc := cleanDescription(resp.Content, c.Words)
	if desc == "" {
		return imgs, fmt.Errorf("empty description")
	}
	if j.ref.cache {
		_ = b.deps.DB.PutMediaDesc(store.MediaDesc{
			Text: desc, UniqueID: j.ref.uniqueID, Kind: j.ref.media, FileID: j.ref.sendID,
			Emoji: j.ref.emoji, Uses: 1, LastSeen: time.Now(),
		}, c.CacheTTL)
	}
	b.applyDescription(j, desc)
	return imgs, nil
}

// applyDescription writes «[label: title — description]» in place of the
// stored media tag.
func (b *Bot) applyDescription(j describeJob, desc string) {
	_ = b.deps.DB.UpdateMessage(j.chatID, j.msgID, func(m *store.Message) {
		if m.Described || !strings.HasPrefix(m.Text, j.tag) {
			return
		}
		m.Text = describedTag(j.tag, desc) + m.Text[len(j.tag):]
		m.Described = true
	})
}

// describedTag puts a description inside a media tag: "[استیکر: 😂]" →
// "[استیکر: 😂 — گربه‌ای که می‌خندد]", "[عکس]" → "[عکس: ساحل در غروب]".
func describedTag(tag, desc string) string {
	inner := strings.TrimSuffix(strings.TrimPrefix(tag, "["), "]")
	if strings.Contains(inner, ": ") {
		return "[" + inner + " — " + desc + "]"
	}
	return "[" + inner + ": " + desc + "]"
}

// cleanDescription makes the model's output one line of at most words+10
// words (some slack: models count loosely), or "" for leaked reasoning.
func cleanDescription(s string, words int) string {
	s = llm.Clean(s)
	if leakedReasoning(s) {
		return ""
	}
	s = strings.Trim(strings.Join(strings.Fields(s), " "), "\"'«»“” ")
	s = strings.NewReplacer("[", "(", "]", ")").Replace(s) // keep the tag well-formed
	if f := strings.Fields(s); len(f) > words+10 {
		s = strings.Join(f[:words+10], " ") + "…"
	}
	return s
}

// maxImageBytes caps a picture download (photos are picked at ~512–1280 px).
const maxImageBytes = 10 << 20

// downloadFile fetches a Telegram file (GetFile + FileDownloadURL) and
// returns its bytes; a file bigger than limit is an error.
func (b *Bot) downloadFile(ctx context.Context, fileID string, limit int64) ([]byte, error) {
	f, err := b.deps.TG.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.deps.TG.FileDownloadURL(f.FilePath), nil)
	if err != nil {
		return nil, fmt.Errorf("download: new request") // the URL has the token: don't log it
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var ue *url.Error // its message has the URL, so the token: keep only the cause
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download: file is over %d bytes", limit)
	}
	return data, nil
}

// downloadImage fetches a Telegram file and returns it as PNG or JPEG (WEBP
// stickers are converted to PNG).
func (b *Bot) downloadImage(ctx context.Context, fileID string) (llm.Image, error) {
	data, err := b.downloadFile(ctx, fileID, maxImageBytes)
	if err != nil {
		return llm.Image{}, err
	}
	return toModelImage(data)
}

// toModelImage passes PNG and JPEG through and converts WEBP (and anything
// else image.Decode knows) to PNG.
func toModelImage(data []byte) (llm.Image, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}):
		return llm.Image{MIME: "image/png", Data: data}, nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return llm.Image{MIME: "image/jpeg", Data: data}, nil
	}
	var img image.Image
	var err error
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		img, err = webp.Decode(bytes.NewReader(data))
	} else {
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return llm.Image{}, fmt.Errorf("decode image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return llm.Image{}, err
	}
	return llm.Image{MIME: "image/png", Data: buf.Bytes()}, nil
}

// questionImage returns the pictures a reply is about, to attach to the reply
// call so the model can answer specific questions about them («پشتش چی نوشته؟»,
// «چند نفرن؟», «این چه رنگیه؟»): those of the message the trigger replies to, else the
// trigger's own (one picture, or the frames of an animated sticker or GIF). The bot's own images
// (faal and poem cards) are skipped: their text is already in the transcript.
// pre is the trigger's own images when describeTrigger just downloaded them.
func (b *Bot) questionImage(ctx context.Context, m *telego.Message, pre []llm.Image) ([]llm.Image, int, bool) {
	if m == nil {
		return nil, 0, false
	}
	for _, c := range []*telego.Message{m.ReplyToMessage, m} {
		if c == nil || (c.From != nil && c.From.IsBot) {
			continue
		}
		ref, ok := b.mediaRefOf(c)
		if !ok {
			continue
		}
		if c == m && len(pre) > 0 {
			return pre, c.MessageID, true
		}
		imgs, _, err := b.mediaImages(ctx, ref)
		if err != nil {
			b.logger.Warn("question image download failed", "chat", m.Chat.ID, "err", err)
			return nil, 0, false
		}
		return imgs, c.MessageID, true
	}
	return nil, 0, false
}

// imageNote tells the model about the attached picture, or the n frames of a clip.
func imageNote(msgID, n int) string {
	what := fmt.Sprintf("The picture of message [%d] is attached.", msgID)
	if n > 1 {
		what = fmt.Sprintf("The %d pictures attached are frames of the animated sticker or GIF of message [%d], in order from start to end.", n, msgID)
	}
	return what + " Look at it to answer the >> message, " +
		"with whatever details it asks about (what is in it, text, colors, people's expressions, …). " +
		"Don't guess who a person you don't know is."
}

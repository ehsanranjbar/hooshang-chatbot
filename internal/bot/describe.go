package bot

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // decoders for image.Decode
	"image/png"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	"golang.org/x/image/webp"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// mediaRef is a describable picture of a message: the photo, the sticker (or
// its thumbnail when it is animated) or a GIF's thumbnail.
type mediaRef struct {
	kind     string // for the prompt: "photo", "sticker", "GIF (its first frame)"
	fileID   string // what to download
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
	c := b.deps.Cfg.Media
	if !c.Enabled {
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
		if s.IsAnimated || s.IsVideo { // TGS / WEBM: only the thumbnail is a picture
			if s.Thumbnail == nil {
				return mediaRef{}, false
			}
			r.fileID = s.Thumbnail.FileID
		}
		return r, true
	case m.Animation != nil && c.GIFs && m.Animation.Thumbnail != nil:
		a := m.Animation
		return mediaRef{kind: "GIF (its first frame)", fileID: a.Thumbnail.FileID, uniqueID: a.FileUniqueID, cache: true, hint: m.Caption,
			media: "gif", sendID: a.FileID}, true
	}
	return mediaRef{}, false
}

// describeJob is one pending description.
type describeJob struct {
	chatID, msgID int64
	tag           string // the stored media tag the description goes into
	ref           mediaRef
}

// describer describes media in the background, one at a time, so replies
// never wait for it.
type describer struct {
	b      *Bot
	queue  chan describeJob
	done   chan struct{}
	mu     sync.Mutex
	active map[string]chan struct{} // file ids being described → closed when done
}

func newDescriber(b *Bot) *describer {
	return &describer{b: b, queue: make(chan describeJob, b.deps.Cfg.Media.MaxQueue), done: make(chan struct{}), active: map[string]chan struct{}{}}
}

// run serves the queue until ctx is done.
func (d *describer) run(ctx context.Context) {
	defer close(d.done)
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-d.queue:
			if wait, ok := d.claim(j.ref.fileID); !ok {
				// The reply path is describing this file right now: wait, then
				// describe() applies the now-cached description to this message.
				select {
				case <-wait:
				case <-ctx.Done():
					return
				}
				if _, ok = d.claim(j.ref.fileID); !ok {
					continue
				}
			}
			if _, err := d.b.describe(ctx, j); err != nil {
				d.b.logger.Warn("media description failed", "chat", j.chatID, "kind", j.ref.kind, "err", err)
			}
			d.release(j.ref.fileID)
		}
	}
}

// claim marks a file as being described. If it already is, ok is false and
// wait is closed when that description is done.
func (d *describer) claim(id string) (wait <-chan struct{}, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ch, busy := d.active[id]; busy {
		return ch, false
	}
	d.active[id] = make(chan struct{})
	return nil, true
}

func (d *describer) release(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ch, ok := d.active[id]; ok {
		close(ch)
		delete(d.active, id)
	}
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
			_ = b.deps.DB.SeenMedia(ref.uniqueID, ref.sendID, ref.emoji, b.deps.Cfg.Media.CacheTTL)
			return
		}
	}
	if b.describer == nil {
		return
	}
	select {
	case b.describer.queue <- j:
	default:
		b.logger.Debug("media description skipped: queue full", "chat", j.chatID)
	}
}

// describeTrigger describes the media of a message the bot is about to answer,
// if it has none yet, so the reply can see it. It returns the image it
// downloaded, if any, so the reply needn't download it again.
func (b *Bot) describeTrigger(ctx context.Context, m *telego.Message) *llm.Image {
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
	img, err := b.describe(ctx, j)
	if err != nil {
		b.logger.Warn("media description failed", "chat", j.chatID, "kind", ref.kind, "err", err)
	}
	return img
}

// describe describes the media of a job (from the cache when it can) and
// writes the description into the stored message. It returns the image when
// it had to download one.
func (b *Bot) describe(ctx context.Context, j describeJob) (*llm.Image, error) {
	if j.ref.cache {
		if c, err := b.deps.DB.GetMediaDesc(j.ref.uniqueID); err == nil {
			b.applyDescription(j, c.Text)
			return nil, nil
		}
	}
	img, err := b.downloadImage(ctx, j.ref.fileID)
	if err != nil {
		return nil, err
	}
	c := b.deps.Cfg.Media
	task, _ := prompts.MediaDescribe(prompts.MediaData{
		Language: b.deps.Cfg.Bot.Language, Kind: j.ref.kind, Hint: j.ref.hint,
		Reaction: j.ref.kind != "photo", Words: c.Words,
	})
	resp, err := b.deps.LLM.Chat(ctx, llm.Request{
		Model:       b.deps.Cfg.LLM.Model,
		Messages:    []llm.Message{{Role: "user", Content: task, Images: []llm.Image{img}}},
		Temperature: 0.2,
		MaxTokens:   b.deps.Cfg.LLM.MaxTokens,
	})
	if err != nil {
		return &img, err
	}
	desc := cleanDescription(resp.Content, c.Words)
	if desc == "" {
		return &img, fmt.Errorf("empty description")
	}
	if j.ref.cache {
		_ = b.deps.DB.PutMediaDesc(store.MediaDesc{
			Text: desc, UniqueID: j.ref.uniqueID, Kind: j.ref.media, FileID: j.ref.sendID,
			Emoji: j.ref.emoji, Uses: 1, LastSeen: time.Now(),
		}, c.CacheTTL)
	}
	b.applyDescription(j, desc)
	return &img, nil
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

// maxImageBytes caps a download (photos are picked at ~512–1280 px).
const maxImageBytes = 10 << 20

// downloadImage fetches a Telegram file and returns it as PNG or JPEG (WEBP
// stickers are converted to PNG).
func (b *Bot) downloadImage(ctx context.Context, fileID string) (llm.Image, error) {
	f, err := b.deps.TG.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return llm.Image{}, fmt.Errorf("get file: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.deps.TG.FileDownloadURL(f.FilePath), nil)
	if err != nil {
		return llm.Image{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return llm.Image{}, fmt.Errorf("download: %w", err) // the URL has the token: don't log it
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return llm.Image{}, fmt.Errorf("download: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return llm.Image{}, fmt.Errorf("download: %w", err)
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

// questionImage returns the picture a reply is about, to attach to the reply
// call so the model can answer specific questions about it («پشتش چی نوشته؟»,
// «چند نفرن؟», «این چه رنگیه؟»): the picture of the message the trigger replies to, else the
// trigger's own. The bot's own images (faal and poem cards) are skipped: their
// text is already in the transcript.
// pre is the trigger's own image when describeTrigger just downloaded it.
func (b *Bot) questionImage(ctx context.Context, m *telego.Message, pre *llm.Image) (llm.Image, int, bool) {
	if m == nil {
		return llm.Image{}, 0, false
	}
	for _, c := range []*telego.Message{m.ReplyToMessage, m} {
		if c == nil || (c.From != nil && c.From.IsBot) {
			continue
		}
		ref, ok := b.mediaRefOf(c)
		if !ok {
			continue
		}
		if c == m && pre != nil {
			return *pre, c.MessageID, true
		}
		img, err := b.downloadImage(ctx, ref.fileID)
		if err != nil {
			b.logger.Warn("question image download failed", "chat", m.Chat.ID, "err", err)
			return llm.Image{}, 0, false
		}
		return img, c.MessageID, true
	}
	return llm.Image{}, 0, false
}

// imageNote tells the model about the attached picture.
func imageNote(msgID int) string {
	return fmt.Sprintf("The picture of message [%d] is attached. Look at it to answer the >> message, "+
		"with whatever details it asks about (what is in it, text, colors, people's expressions, …). "+
		"Don't guess who a person you don't know is.", msgID)
}

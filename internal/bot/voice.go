package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/store"
	"github.com/ehsanranjbar/hooshang/internal/transcribe"
)

// voiceRef is a transcribable recording of a message.
type voiceRef struct {
	fileID string
	mime   string
}

// voiceOn reports whether voice messages are transcribed at all.
func (b *Bot) voiceOn() bool {
	return b.deps.Cfg.Features.Voice.On() && b.deps.Transcriber != nil
}

// newTranscriber returns the background queue of transcriptions.
func newTranscriber(b *Bot) *enricher {
	return newEnricher(b.deps.Cfg.Features.Voice.MaxQueue)
}

// voiceRefOf returns the message's voice message (or video note, if enabled),
// unless it is longer than max_duration or too big to download.
func (b *Bot) voiceRefOf(m *telego.Message) (voiceRef, bool) {
	if !b.voiceOn() {
		return voiceRef{}, false
	}
	c := b.deps.Cfg.Features.Voice
	var ref voiceRef
	var seconds int
	var size int64
	switch {
	case m.Voice != nil:
		ref, seconds, size = voiceRef{m.Voice.FileID, mimeOr(m.Voice.MimeType, "audio/ogg")}, m.Voice.Duration, m.Voice.FileSize
	case m.VideoNote != nil && c.VideoNotes:
		ref, seconds, size = voiceRef{m.VideoNote.FileID, "video/mp4"}, m.VideoNote.Duration, int64(m.VideoNote.FileSize)
	default:
		return voiceRef{}, false
	}
	if ref.fileID == "" || time.Duration(seconds)*time.Second > c.MaxDuration || size > maxClipBytes {
		return voiceRef{}, false
	}
	return ref, true
}

// mimeOr returns mime, or def when it is empty.
func mimeOr(mime, def string) string {
	if mime == "" {
		return def
	}
	return mime
}

// transcribeIncoming queues the transcription of a stored voice message and
// reports whether it was queued (it is not when the queue is full). With
// answer, the message is then handled as step 7 of handleMessage, now that
// its words are known: a reply when it addresses the bot.
func (b *Bot) transcribeIncoming(m *telego.Message, ref voiceRef, answer bool) bool {
	if b.transcriber == nil {
		return false
	}
	queued := b.transcriber.submit(ref.fileID, func(ctx context.Context) {
		text, err := b.transcribe(ctx, m, ref)
		if err != nil {
			b.logger.Warn("voice transcription failed", "chat", m.Chat.ID, "err", err)
		}
		if answer && ctx.Err() == nil {
			b.replyOrSpontaneous(m, text)
		}
	})
	if !queued {
		b.logger.Debug("voice transcription skipped: queue full", "chat", m.Chat.ID)
	}
	return queued
}

// transcribeTrigger transcribes the voice message the bot is about to answer,
// if it has no transcript yet, so the reply can see what was said. Like
// describeTrigger, it waits for the background worker when that has the file.
func (b *Bot) transcribeTrigger(ctx context.Context, m *telego.Message) {
	if m == nil {
		return
	}
	ref, ok := b.voiceRefOf(m)
	if !ok {
		return
	}
	if s, err := b.deps.DB.GetMessage(m.Chat.ID, int64(m.MessageID)); err != nil || s.Described {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, b.deps.Cfg.LLM.Timeout)
	defer cancel()
	if e := b.transcriber; e != nil {
		wait, ok := e.claim(ref.fileID)
		if !ok { // the background worker has it: wait instead of transcribing it twice
			select {
			case <-wait:
			case <-ctx.Done():
			}
			return
		}
		defer e.release(ref.fileID)
	}
	if _, err := b.transcribe(ctx, m, ref); err != nil {
		b.logger.Warn("voice transcription failed", "chat", m.Chat.ID, "err", err)
	}
}

// transcribe downloads and transcribes a recording and writes the text into
// the stored message: «[ویس]» becomes «[ویس: متن]». It returns the text (empty
// when there were no words). A message that already has its transcript is left
// alone.
func (b *Bot) transcribe(ctx context.Context, m *telego.Message, ref voiceRef) (string, error) {
	if s, err := b.deps.DB.GetMessage(m.Chat.ID, int64(m.MessageID)); err != nil || s.Described {
		return "", nil
	}
	audio, err := b.downloadFile(ctx, ref.fileID, maxClipBytes)
	if err != nil {
		return "", err
	}
	text, err := b.deps.Transcriber.Transcribe(ctx, audio, ref.mime)
	if err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}
	text = cleanTranscript(text, b.deps.Cfg.Features.Voice.MaxWords)
	j := describeJob{chatID: m.Chat.ID, msgID: int64(m.MessageID), tag: mediaTag(m)}
	if text == "" { // music or silence: leave the tag, but don't try again
		b.markDescribed(j)
		return "", nil
	}
	b.applyDescription(j, text)
	return text, nil
}

// markDescribed marks a message as done without changing its text.
func (b *Bot) markDescribed(j describeJob) {
	_ = b.deps.DB.UpdateMessage(j.chatID, j.msgID, func(m *store.Message) { m.Described = true })
}

// cleanTranscript makes a transcript fit inside a media tag: one line of at
// most words words, with no square brackets.
func cleanTranscript(s string, words int) string {
	s = transcribe.Clean(s, words)
	return strings.NewReplacer("[", "(", "]", ")").Replace(s)
}

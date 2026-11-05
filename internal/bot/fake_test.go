package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mymmrac/telego"
)

// fakeTG is a scripted TG for tests. It records sent messages and chat
// actions, and answers GetChatMember with the configured member (or error).
type fakeTG struct {
	mu sync.Mutex

	sent    []*telego.SendMessageParams
	photos  []*telego.SendPhotoParams
	actions []*telego.SendChatActionParams
	leaves  []*telego.LeaveChatParams

	// member is returned by GetChatMember; memberErr makes it return an error.
	member    telego.ChatMember
	memberErr error

	sendErr error // returned by SendMessage (e.g. a 429)

	// files are served by GetFile + FileDownloadURL: file id → content.
	files    map[string][]byte
	fileURL  string // base URL of the test server that serves files
	getFiles int    // GetFile calls

	reactions  []*telego.SetMessageReactionParams
	stickers   []*telego.SendStickerParams
	animations []*telego.SendAnimationParams

	// Moderation calls, recorded in order; modErr makes every one of them fail.
	bans          []*telego.BanChatMemberParams
	unbans        []*telego.UnbanChatMemberParams
	restricts     []*telego.RestrictChatMemberParams
	deletes       []*telego.DeleteMessageParams
	bulkDeletes   []*telego.DeleteMessagesParams
	pins          []*telego.PinChatMessageParams
	unpins        []*telego.UnpinChatMessageParams
	photoLookups  []*telego.GetUserProfilePhotosParams
	chatLookups   []*telego.GetChatParams
	modErr        error
	chatInfo      *telego.ChatFullInfo      // returned by GetChat
	profilePhotos *telego.UserProfilePhotos // returned by GetUserProfilePhotos (nil = none)

	nextMsgID int
}

// newFakeTG returns a fakeTG that reports the owner as a member by default.
func newFakeTG() *fakeTG {
	return &fakeTG{nextMsgID: 1000}
}

func (f *fakeTG) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.sent = append(f.sent, p)
	f.nextMsgID++
	return &telego.Message{
		MessageID: f.nextMsgID,
		Chat:      telego.Chat{ID: p.ChatID.ID},
		From:      &telego.User{IsBot: true},
		Date:      0,
		Text:      p.Text,
	}, nil
}

func (f *fakeTG) SendPhoto(_ context.Context, p *telego.SendPhotoParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.photos = append(f.photos, p)
	f.nextMsgID++
	return &telego.Message{
		MessageID: f.nextMsgID,
		Chat:      telego.Chat{ID: p.ChatID.ID},
		From:      &telego.User{IsBot: true},
		Caption:   p.Caption,
		Photo:     []telego.PhotoSize{{}},
	}, nil
}

func (f *fakeTG) SendChatAction(_ context.Context, p *telego.SendChatActionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, p)
	return nil
}

func (f *fakeTG) GetChatMember(_ context.Context, _ *telego.GetChatMemberParams) (telego.ChatMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.member, f.memberErr
}

func (f *fakeTG) LeaveChat(_ context.Context, p *telego.LeaveChatParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaves = append(f.leaves, p)
	return nil
}

// sentTexts returns the texts of the recorded messages in order.
func (f *fakeTG) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	for i, p := range f.sent {
		out[i] = p.Text
	}
	return out
}

// sentCount returns how many messages were sent.
func (f *fakeTG) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// leaveCount returns how many LeaveChat calls were made.
func (f *fakeTG) leaveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.leaves)
}

func (f *fakeTG) GetFile(_ context.Context, p *telego.GetFileParams) (*telego.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getFiles++
	if _, ok := f.files[p.FileID]; !ok {
		return nil, errors.New("file not found")
	}
	return &telego.File{FileID: p.FileID, FilePath: p.FileID}, nil
}

func (f *fakeTG) FileDownloadURL(path string) string { return f.fileURL + "/" + path }

// serveFiles starts a server for f.files.
func (f *fakeTG) serveFiles(t *testing.T, files map[string][]byte) {
	t.Helper()
	f.files = files
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		data, ok := f.files[strings.TrimPrefix(r.URL.Path, "/")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	f.fileURL = srv.URL
}

func (f *fakeTG) SetMessageReaction(_ context.Context, p *telego.SetMessageReactionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, p)
	return nil
}

// reacted returns the emojis the bot reacted with, in order.
func (f *fakeTG) reacted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reactions {
		for _, t := range r.Reaction {
			if e, ok := t.(*telego.ReactionTypeEmoji); ok {
				out = append(out, e.Emoji)
			}
		}
	}
	return out
}

func (f *fakeTG) SendSticker(_ context.Context, p *telego.SendStickerParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stickers = append(f.stickers, p)
	f.nextMsgID++
	return &telego.Message{MessageID: f.nextMsgID, Chat: telego.Chat{ID: p.ChatID.ID}, From: &telego.User{IsBot: true},
		Sticker: &telego.Sticker{FileID: p.Sticker.FileID, Emoji: p.Emoji}}, nil
}

func (f *fakeTG) SendAnimation(_ context.Context, p *telego.SendAnimationParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.animations = append(f.animations, p)
	f.nextMsgID++
	return &telego.Message{MessageID: f.nextMsgID, Chat: telego.Chat{ID: p.ChatID.ID}, From: &telego.User{IsBot: true},
		Animation: &telego.Animation{FileID: p.Animation.FileID}}, nil
}

func (f *fakeTG) BanChatMember(_ context.Context, p *telego.BanChatMemberParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bans = append(f.bans, p)
	return f.modErr
}

func (f *fakeTG) UnbanChatMember(_ context.Context, p *telego.UnbanChatMemberParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unbans = append(f.unbans, p)
	return f.modErr
}

func (f *fakeTG) RestrictChatMember(_ context.Context, p *telego.RestrictChatMemberParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restricts = append(f.restricts, p)
	return f.modErr
}

func (f *fakeTG) DeleteMessage(_ context.Context, p *telego.DeleteMessageParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, p)
	return f.modErr
}

func (f *fakeTG) DeleteMessages(_ context.Context, p *telego.DeleteMessagesParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bulkDeletes = append(f.bulkDeletes, p)
	return f.modErr
}

func (f *fakeTG) PinChatMessage(_ context.Context, p *telego.PinChatMessageParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins = append(f.pins, p)
	return f.modErr
}

func (f *fakeTG) UnpinChatMessage(_ context.Context, p *telego.UnpinChatMessageParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unpins = append(f.unpins, p)
	return f.modErr
}

func (f *fakeTG) GetUserProfilePhotos(_ context.Context, p *telego.GetUserProfilePhotosParams) (*telego.UserProfilePhotos, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.photoLookups = append(f.photoLookups, p)
	if f.profilePhotos != nil {
		return f.profilePhotos, nil
	}
	return &telego.UserProfilePhotos{}, nil
}

func (f *fakeTG) GetChat(_ context.Context, p *telego.GetChatParams) (*telego.ChatFullInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatLookups = append(f.chatLookups, p)
	if f.chatInfo != nil {
		return f.chatInfo, nil
	}
	return &telego.ChatFullInfo{ID: p.ChatID.ID}, nil
}

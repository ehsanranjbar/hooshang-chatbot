package bot

import (
	"context"
	"errors"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

// TG is the narrow surface of telego.Bot that the bot uses. *telego.Bot
// satisfies it, so tests can substitute a fake.
type TG interface {
	SendMessage(ctx context.Context, p *telego.SendMessageParams) (*telego.Message, error)
	SendPhoto(ctx context.Context, p *telego.SendPhotoParams) (*telego.Message, error)
	SendChatAction(ctx context.Context, p *telego.SendChatActionParams) error
	GetChatMember(ctx context.Context, p *telego.GetChatMemberParams) (telego.ChatMember, error)
	LeaveChat(ctx context.Context, p *telego.LeaveChatParams) error
	GetFile(ctx context.Context, p *telego.GetFileParams) (*telego.File, error)
	FileDownloadURL(filepath string) string
	SetMessageReaction(ctx context.Context, p *telego.SetMessageReactionParams) error
	SendSticker(ctx context.Context, p *telego.SendStickerParams) (*telego.Message, error)
	SendAnimation(ctx context.Context, p *telego.SendAnimationParams) (*telego.Message, error)

	// Moderation (features.moderation).
	BanChatMember(ctx context.Context, p *telego.BanChatMemberParams) error
	UnbanChatMember(ctx context.Context, p *telego.UnbanChatMemberParams) error
	RestrictChatMember(ctx context.Context, p *telego.RestrictChatMemberParams) error
	DeleteMessage(ctx context.Context, p *telego.DeleteMessageParams) error
	DeleteMessages(ctx context.Context, p *telego.DeleteMessagesParams) error
	PinChatMessage(ctx context.Context, p *telego.PinChatMessageParams) error
	UnpinChatMessage(ctx context.Context, p *telego.UnpinChatMessageParams) error

	// Wired for a later phase (profile photos, chat details); nothing calls them yet.
	GetUserProfilePhotos(ctx context.Context, p *telego.GetUserProfilePhotosParams) (*telego.UserProfilePhotos, error)
	GetChat(ctx context.Context, p *telego.GetChatParams) (*telego.ChatFullInfo, error)
}

// errConcreteTG is returned when a concrete *telego.Bot is required (polling,
// GetMe) but a fake was provided.
var errConcreteTG = errors.New("bot: a concrete *telego.Bot is required here")

// isFloodControl reports whether a telego API error is a 429 flood-control
// error.
func isFloodControl(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *ta.Error
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode == 429
	}
	return false
}

// newChatID builds a telego ChatID from an int64.
func newChatID(id int64) telego.ChatID { return telego.ChatID{ID: id} }

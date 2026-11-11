package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// minMute is the shortest mute: Telegram treats a restriction under 30
// seconds as permanent, so a minute is the floor.
const minMute = time.Minute

// recentScan is how many of the chat's latest stored messages are searched
// for a member's messages to delete.
const recentScan = 1000

// refusal is a moderation request the code declined (as opposed to Telegram
// failing); the model gets "refused: …" for it.
type refusal string

// Error implements error.
func (r refusal) Error() string { return string(r) }

// moderationTools are the definitions of the admin-only moderation tools.
func moderationTools() []llm.Tool {
	return []llm.Tool{
		{Name: "moderate", Description: "Moderate the group at the admin's explicit order: delete messages, mute (silence) a member for a while, kick (remove, they may rejoin), ban, unban, or pin/unpin a message. It acts at once. For delete and pin without a named member, pass message_id: the message the admin replied to (↩︎[id] in the transcript). Never call it for a non-admin's request.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"action":{"type":"string","enum":["delete","mute","kick","ban","unban","pin","unpin"]},` +
				`"member":{"type":"string","description":"name, @username or numeric id; empty means the member the admin replied to; optional for delete/pin when message_id is given"},` +
				`"message_id":{"type":"integer","description":"the message to delete, pin or unpin (the one the admin replied to, shown as ↩︎[id])"},` +
				`"count":{"type":"integer","description":"delete this many of the member's recent messages (default 1)"},` +
				`"duration":{"type":"string","description":"mute length such as 30m, 2h, 1d (a default is used when omitted)"},` +
				`"reason":{"type":"string","description":"short reason, optional"}},"required":["action"]}`)},
		{Name: "set_moderation", Description: "Change which moderation actions you may do in this group, when the admin says what you may or may not do («بن نکن», «فقط حذف کن», «دوباره می‌تونی بن کنی»). Lists are action names: delete, mute, kick, ban, unban, pin, unpin.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"allow":{"type":"array","items":{"type":"string","enum":["delete","mute","kick","ban","unban","pin","unpin"]}},` +
				`"deny":{"type":"array","items":{"type":"string","enum":["delete","mute","kick","ban","unban","pin","unpin"]}}}}`)},
	}
}

// modArgs are the arguments of the moderate tool. The numbers are json.Number
// because models sometimes send "123" instead of 123.
type modArgs struct {
	Action    string      `json:"action"`
	Member    string      `json:"member"`
	MessageID json.Number `json:"message_id"`
	Count     json.Number `json:"count"`
	Duration  string      `json:"duration"`
	Reason    string      `json:"reason"`
}

// toolModerate is the moderate tool handler. Every call that gets as far as
// a valid action is written to the mod_log, also when it was refused or
// Telegram failed.
func (b *Bot) toolModerate(j job) llm.ToolFunc {
	return func(ctx context.Context, args string) (string, error) {
		var a modArgs
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		a.Action = strings.ToLower(strings.TrimSpace(a.Action))
		entry := store.ModEntry{AdminID: j.authorID(), Action: a.Action, Reason: strings.TrimSpace(a.Reason), At: time.Now()}
		line, err := b.moderate(ctx, j, a, &entry)
		var ref refusal
		switch {
		case err == nil:
		case errors.As(err, &ref):
			entry.Err, line = "refused: "+string(ref), "refused: "+string(ref)
		default:
			entry.Err, line = err.Error(), "telegram error: "+err.Error()
			b.logger.Warn("moderation failed", "chat", j.chatID, "action", a.Action, "err", err)
		}
		if err := b.deps.DB.AddModEntry(j.chatID, &entry); err != nil {
			b.logger.Error("mod_log write failed", "chat", j.chatID, "err", err)
		}
		return line, nil
	}
}

// moderate checks the request in code (the model is never trusted with
// these) and runs it. A refusal is returned as a refusal error, a Telegram
// failure as itself; entry is filled in as far as it got.
func (b *Bot) moderate(ctx context.Context, j job, a modArgs, entry *store.ModEntry) (string, error) {
	if j.authorID() <= 0 || !b.deps.Admins.IsAdmin(j.chatID, j.authorID()) {
		return "", refusal("only the admin can moderate")
	}
	if !contains(config.ModerationActions, a.Action) {
		return "", refusal(fmt.Sprintf("unknown action %q (actions: %s)", a.Action, strings.Join(config.ModerationActions, ", ")))
	}
	chat, _ := b.deps.DB.GetChat(j.chatID)
	if !b.allowedActions(chat)[a.Action] {
		return "", refusal(a.Action + " is not allowed in this group (the admin turned it off)")
	}
	switch a.Action {
	case "delete":
		return b.modDelete(ctx, j, a, entry)
	case "pin", "unpin":
		return b.modPin(ctx, j, a, entry)
	}
	target, name, err := b.modTarget(j, a.Member)
	if err != nil {
		return "", err
	}
	entry.TargetID, entry.TargetName = target, name
	switch a.Action {
	case "mute":
		return b.modMute(ctx, j, a, entry)
	case "kick":
		return b.modKick(ctx, j, name, target)
	case "ban":
		return "done: banned " + name, b.deps.TG.BanChatMember(ctx, &telego.BanChatMemberParams{ChatID: newChatID(j.chatID), UserID: target})
	default: // unban
		return "done: unbanned " + name, b.deps.TG.UnbanChatMember(ctx, &telego.UnbanChatMemberParams{ChatID: newChatID(j.chatID), UserID: target, OnlyIfBanned: true})
	}
}

// modTarget resolves the member an action is aimed at and refuses the ones
// that must never be moderated: an admin and the bot itself.
// Members who are ignored resolve too.
func (b *Bot) modTarget(j job, ref string) (int64, string, error) {
	id, err := b.resolveModerationTarget(j.chatID, ref, j)
	if err != nil {
		return 0, "", refusal(strings.TrimPrefix(err.Error(), "error: "))
	}
	name := b.modName(j.chatID, id)
	switch {
	case id == b.botID:
		return 0, "", refusal("that is me, I don't moderate myself")
	case b.deps.Admins.IsAdmin(j.chatID, id):
		return 0, "", refusal(name + " is an admin")
	}
	return id, name, nil
}

// modName is the display name of a user for the log and the tool result.
func (b *Bot) modName(chatID, id int64) string {
	if mem, err := b.deps.DB.GetMember(chatID, id); err == nil && mem != nil && displayName(mem) != "" {
		return displayName(mem)
	}
	return strconv.FormatInt(id, 10)
}

// allowedActions is the set of actions the bot may do in the chat: the
// config list (features.moderation.actions) is the ceiling; within it, the
// admin's runtime set (set_moderation) when there is one.
func (b *Bot) allowedActions(c store.Chat) map[string]bool {
	ceiling := map[string]bool{}
	if m := b.deps.Cfg.Features.Moderation; m.On() {
		for _, a := range m.Actions {
			ceiling[a] = true
		}
	}
	if c.ModActions == nil {
		return ceiling
	}
	set := map[string]bool{} // the admin's runtime set, within the config's ceiling
	for a, ok := range c.ModActions {
		if ok && ceiling[a] {
			set[a] = true
		}
	}
	return set
}

// actionList renders an allowed set in the fixed action order.
func actionList(set map[string]bool) string {
	var on []string
	for _, a := range config.ModerationActions {
		if set[a] {
			on = append(on, a)
		}
	}
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, ", ")
}

// modMute restricts the member from sending anything until the clamped end.
func (b *Bot) modMute(ctx context.Context, j job, a modArgs, entry *store.ModEntry) (string, error) {
	m := b.deps.Cfg.Features.Moderation
	d, err := parseModDuration(a.Duration)
	if err != nil {
		return "", refusal(err.Error())
	}
	if d == 0 {
		d = m.DefaultMute
	}
	d = min(max(d, minMute), m.MaxMute)
	until := time.Now().Add(d)
	entry.Until = until
	err = b.deps.TG.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:      newChatID(j.chatID),
		UserID:      entry.TargetID,
		Permissions: noPermissions(),
		UntilDate:   until.Unix(),
	})
	return fmt.Sprintf("done: muted %s until %s", entry.TargetName, prompts.FormatNow(until)), err
}

// modKick removes the member but lets them rejoin: a ban, then an unban.
func (b *Bot) modKick(ctx context.Context, j job, name string, target int64) (string, error) {
	chat := newChatID(j.chatID)
	if err := b.deps.TG.BanChatMember(ctx, &telego.BanChatMemberParams{ChatID: chat, UserID: target}); err != nil {
		return "", err
	}
	if err := b.deps.TG.UnbanChatMember(ctx, &telego.UnbanChatMemberParams{ChatID: chat, UserID: target}); err != nil {
		return "", fmt.Errorf("%s was removed but the unban failed, so they are banned: %w", name, err)
	}
	return "done: kicked " + name, nil
}

// noPermissions is a permission set with everything off (a full mute).
func noPermissions() telego.ChatPermissions {
	off := telego.ToPtr(false)
	return telego.ChatPermissions{
		CanSendMessages: off, CanSendAudios: off, CanSendDocuments: off, CanSendPhotos: off,
		CanSendVideos: off, CanSendVideoNotes: off, CanSendVoiceNotes: off, CanSendPolls: off,
		CanSendOtherMessages: off, CanAddWebPagePreviews: off, CanChangeInfo: off,
		CanInviteUsers: off, CanPinMessages: off, CanManageTopics: off,
	}
}

// parseModDuration reads a mute length such as 30m, 2h, 1d, 1w or 1h30m.
// Empty is 0 (use the default).
func parseModDuration(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("bad duration %q (use e.g. 30m, 2h, 1d)", s)
		}
		return d, nil
	}
	n, unit := s[:len(s)-1], s[len(s)-1]
	f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("bad duration %q (use e.g. 30m, 2h, 1d)", s)
	}
	switch unit {
	case 'd':
		return time.Duration(f * float64(24*time.Hour)), nil
	case 'w':
		return time.Duration(f * float64(7*24*time.Hour)), nil
	}
	return 0, fmt.Errorf("bad duration %q (use e.g. 30m, 2h, 1d)", s)
}

// numArg reads a model-supplied integer; anything unreadable is 0.
func numArg(n json.Number) int64 {
	if n == "" {
		return 0
	}
	if v, err := n.Int64(); err == nil {
		return v
	}
	if f, err := n.Float64(); err == nil {
		return int64(f)
	}
	return 0
}

// targetMessage is the message a delete, pin or unpin is about: message_id,
// else the message the admin replied to; 0 when neither is there.
func targetMessage(j job, a modArgs) int {
	if id := numArg(a.MessageID); id > 0 {
		return int(id)
	}
	return replyMessageID(j.msg)
}

// modPin pins or unpins a message (pinned silently).
func (b *Bot) modPin(ctx context.Context, j job, a modArgs, entry *store.ModEntry) (string, error) {
	id := targetMessage(j, a)
	chat := newChatID(j.chatID)
	if a.Action == "pin" {
		if id == 0 {
			return "", refusal("which message? give message_id or reply to it")
		}
		entry.MsgIDs = []int64{int64(id)}
		err := b.deps.TG.PinChatMessage(ctx, &telego.PinChatMessageParams{ChatID: chat, MessageID: id, DisableNotification: true})
		return fmt.Sprintf("done: pinned message %d", id), err
	}
	if id > 0 { // without an id Telegram unpins the most recent pinned message
		entry.MsgIDs = []int64{int64(id)}
	}
	err := b.deps.TG.UnpinChatMessage(ctx, &telego.UnpinChatMessageParams{ChatID: chat, MessageID: id})
	if id == 0 {
		return "done: unpinned the most recent pinned message", err
	}
	return fmt.Sprintf("done: unpinned message %d", id), err
}

// modDelete deletes one message (message_id, or the replied-to one), or the
// last `count` stored messages of a member. Naming a member, or a count above
// one, makes the action about that member, who is checked like any target.
func (b *Bot) modDelete(ctx context.Context, j job, a modArgs, entry *store.ModEntry) (string, error) {
	count := int(min(max(numArg(a.Count), 1), int64(b.deps.Cfg.Features.Moderation.MaxDelete)))
	member := strings.TrimSpace(a.Member)
	aboutMember := member != "" || count > 1
	if aboutMember {
		id, name, err := b.modTarget(j, member)
		if err != nil {
			return "", err
		}
		entry.TargetID, entry.TargetName = id, name
	}
	var ids []int64
	switch {
	case !aboutMember, count == 1 && numArg(a.MessageID) > 0:
		if id := targetMessage(j, a); id > 0 {
			ids = []int64{int64(id)}
		}
	case count == 1 && replyMessageID(j.msg) > 0 && replyUserID(j.msg) == entry.TargetID:
		ids = []int64{int64(replyMessageID(j.msg))}
	default:
		ids = b.recentOf(j, entry.TargetID, count)
	}
	if len(ids) == 0 {
		return "", refusal("nothing to delete: give message_id, reply to the message, or name a member who has recent messages")
	}
	entry.MsgIDs = ids
	if err := b.deleteMessages(ctx, j.chatID, ids); err != nil {
		return "", err
	}
	_ = b.deps.DB.DeleteMessageIDs(j.chatID, ids) // they must not stay in the transcript
	if entry.TargetName != "" {
		return fmt.Sprintf("done: deleted %d message(s) of %s", len(ids), entry.TargetName), nil
	}
	return fmt.Sprintf("done: deleted %d message(s)", len(ids)), nil
}

// recentOf returns the ids of the last n stored messages of a member, oldest
// first, never the command message itself.
func (b *Bot) recentOf(j job, userID int64, n int) []int64 {
	msgs, err := b.deps.DB.RecentMessages(j.chatID, recentScan)
	if err != nil {
		return nil
	}
	skip := int64(0)
	if j.msg != nil {
		skip = int64(j.msg.MessageID)
	}
	var ids []int64
	for i := len(msgs) - 1; i >= 0 && len(ids) < n; i-- {
		if msgs[i].UserID == userID && !msgs[i].FromBot && msgs[i].ID != skip {
			ids = append([]int64{msgs[i].ID}, ids...)
		}
	}
	return ids
}

// deleteMessages deletes one message with DeleteMessage, several at once with
// DeleteMessages.
func (b *Bot) deleteMessages(ctx context.Context, chatID int64, ids []int64) error {
	if len(ids) == 1 {
		return b.deps.TG.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: newChatID(chatID), MessageID: int(ids[0])})
	}
	list := make([]int, len(ids))
	for i, id := range ids {
		list[i] = int(id)
	}
	return b.deps.TG.DeleteMessages(ctx, &telego.DeleteMessagesParams{ChatID: newChatID(chatID), MessageIDs: list})
}

// toolSetModeration is the set_moderation tool handler: it changes the
// chat's allowed set starting from the effective one.
func (b *Bot) toolSetModeration(j job) llm.ToolFunc {
	return func(_ context.Context, args string) (string, error) {
		var a struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		if j.authorID() <= 0 || !b.deps.Admins.IsAdmin(j.chatID, j.authorID()) {
			return "refused: only the admin can change this", nil
		}
		for _, name := range append(append([]string(nil), a.Allow...), a.Deny...) {
			if !contains(config.ModerationActions, strings.ToLower(strings.TrimSpace(name))) {
				return fmt.Sprintf("error: unknown action %q (actions: %s)", name, strings.Join(config.ModerationActions, ", ")), nil
			}
		}
		var result map[string]bool
		err := b.deps.DB.UpdateChat(j.chatID, func(c *store.Chat) {
			set := map[string]bool{}
			for k, v := range b.allowedActions(*c) {
				set[k] = v
			}
			for _, name := range a.Allow {
				set[strings.ToLower(strings.TrimSpace(name))] = true
			}
			for _, name := range a.Deny {
				delete(set, strings.ToLower(strings.TrimSpace(name)))
			}
			c.ModActions, result = set, set
		})
		if err != nil {
			return "error: " + err.Error(), nil
		}
		return "done: moderation actions allowed now: " + actionList(result), nil
	}
}

// contains reports whether list has s.
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

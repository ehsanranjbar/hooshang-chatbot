package bot

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
)

// maxTitle caps a media title (file names and song titles can be long).
const maxTitle = 80

// messageText is what the bot stores and shows the model for a message: the
// text, or for media "[label: title] caption", e.g. "[آهنگ: شجریان – بوی باران]".
// isService is true for messages with neither (joins, pins, ...), which are ignored.
func messageText(m *telego.Message) (text string, isService bool) {
	body := m.Text
	if body == "" {
		body = m.Caption
	}
	tag := mediaTag(m)
	switch {
	case tag == "" && body == "":
		return "", true
	case tag == "":
		return body, false
	case body == "":
		return tag, false
	default:
		return tag + " " + body, false
	}
}

// mediaTag describes the media of a message with its title, or "" for none.
func mediaTag(m *telego.Message) string {
	switch {
	case len(m.Photo) > 0:
		return tag(i18n.MediaPhoto, "")
	case m.Sticker != nil:
		return tag(i18n.MediaSticker, m.Sticker.Emoji)
	case m.Audio != nil:
		title := joinNonEmpty(" – ", m.Audio.Performer, m.Audio.Title)
		if title == "" {
			title = m.Audio.FileName
		}
		return tag(i18n.MediaAudio, title)
	case m.Voice != nil:
		return tag(i18n.MediaVoice, "")
	case m.VideoNote != nil:
		return tag(i18n.MediaVideoNote, "")
	case m.Animation != nil:
		return tag(i18n.MediaGIF, "") // GIF file names are generated ("animation.mp4")
	case m.Video != nil:
		return tag(i18n.MediaVideo, m.Video.FileName)
	case m.Document != nil:
		return tag(i18n.MediaFile, m.Document.FileName)
	case m.Poll != nil:
		return tag(i18n.MediaPoll, m.Poll.Question)
	case m.Venue != nil:
		return tag(i18n.MediaVenue, joinNonEmpty("، ", m.Venue.Title, m.Venue.Address))
	case m.Location != nil:
		return tag(i18n.MediaLocation, "")
	case m.Contact != nil:
		return tag(i18n.MediaContact, joinNonEmpty(" ", m.Contact.FirstName, m.Contact.LastName))
	case m.Dice != nil:
		return tag(i18n.MediaDice, fmt.Sprintf("%s %d", m.Dice.Emoji, m.Dice.Value))
	}
	return ""
}

// tag renders "[label]" or "[label: title]".
func tag(label, title string) string {
	title = strings.Join(strings.Fields(title), " ") // no newlines inside a tag
	if r := []rune(title); len(r) > maxTitle {
		title = string(r[:maxTitle]) + "…"
	}
	if title == "" {
		return "[" + label + "]"
	}
	return "[" + label + ": " + title + "]"
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

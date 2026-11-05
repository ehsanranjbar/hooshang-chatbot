package bot

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

func TestMessageTextForMedia(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    telego.Message
		want string
	}{
		{"text", telego.Message{Text: "سلام"}, "سلام"},
		{"photo with caption", telego.Message{Photo: []telego.PhotoSize{{}}, Caption: "ببینید چه منظره‌ای"}, "[عکس] ببینید چه منظره‌ای"},
		{"sticker", telego.Message{Sticker: &telego.Sticker{Emoji: "😂"}}, "[استیکر: 😂]"},
		{"song", telego.Message{Audio: &telego.Audio{Performer: "شجریان", Title: "بوی باران", FileName: "x.mp3"}}, "[آهنگ: شجریان – بوی باران]"},
		{"song without tags", telego.Message{Audio: &telego.Audio{FileName: "track01.mp3"}}, "[آهنگ: track01.mp3]"},
		{"document", telego.Message{Document: &telego.Document{FileName: "جزوه ترمودینامیک.pdf"}, Caption: "برای فردا"}, "[فایل: جزوه ترمودینامیک.pdf] برای فردا"},
		{"video", telego.Message{Video: &telego.Video{FileName: "goal.mp4"}}, "[ویدیو: goal.mp4]"},
		{"gif", telego.Message{Animation: &telego.Animation{FileName: "animation.mp4"}, Document: &telego.Document{FileName: "animation.mp4"}}, "[گیف]"},
		{"poll", telego.Message{Poll: &telego.Poll{Question: "شام چی بخوریم؟"}}, "[نظرسنجی: شام چی بخوریم؟]"},
		{"venue", telego.Message{Venue: &telego.Venue{Title: "کافه لمیز", Address: "ولیعصر"}, Location: &telego.Location{}}, "[مکان: کافه لمیز، ولیعصر]"},
		{"voice", telego.Message{Voice: &telego.Voice{}}, "[ویس]"},
		{"dice", telego.Message{Dice: &telego.Dice{Emoji: "🎲", Value: 6}}, "[تاس: 🎲 6]"},
	} {
		got, service := messageText(&tc.m)
		if service || got != tc.want {
			t.Errorf("%s: got %q (service=%v), want %q", tc.name, got, service, tc.want)
		}
	}
	if _, service := messageText(&telego.Message{}); !service {
		t.Error("an empty message must count as a service message")
	}
}

func TestLongMediaTitlesAreCut(t *testing.T) {
	got, _ := messageText(&telego.Message{Document: &telego.Document{FileName: strings.Repeat("الف", 100) + "\n.pdf"}})
	if n := len([]rune(got)); n > maxTitle+12 || strings.Contains(got, "\n") {
		t.Fatalf("tag = %q (%d runes)", got, n)
	}
}

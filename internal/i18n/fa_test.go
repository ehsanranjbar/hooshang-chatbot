package i18n

import (
	"strings"
	"testing"
)

func TestHelpListsAllCommands(t *testing.T) {
	for _, cmd := range []string{"/faal", "/poem", "/roast", "/me", "/notease", "/teaseok", "/forgetme", "/mute", "/unmute", "/status", "/help"} {
		if !strings.Contains(Help, cmd) {
			t.Errorf("Help is missing %s", cmd)
		}
	}
	if !strings.Contains(Help, "هوشنگ") {
		t.Error("Help should mention the bot name")
	}
}

func TestStringsNonEmptyAndDistinct(t *testing.T) {
	all := []string{
		MediaPhoto, MediaSticker, MediaVoice, MediaVideo, MediaVideoNote, MediaAudio, MediaFile,
		MediaGIF, MediaPoll, MediaLocation, MediaVenue, MediaContact, MediaDice,
		Help, NotAllowedHere, GanjoorDown, UnknownPoet, NoTeaseTarget,
		RoastWho, NothingYet, RateLimited, FaalHeader, MeaningLabel,
		TabirPrefix, PoemLink, NoTeaseOn, NoTeaseOff, Forgotten,
		Muted, Unmuted,
	}
	seen := make(map[string]bool)
	for _, s := range all {
		if s == "" {
			t.Error("empty string")
			continue
		}
		if seen[s] {
			t.Errorf("duplicate string %q", s)
		}
		seen[s] = true
	}
}

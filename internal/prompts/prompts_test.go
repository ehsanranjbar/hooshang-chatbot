package prompts

import (
	"strings"
	"testing"
	"time"
)

func TestSystemRenders(t *testing.T) {
	out, err := System(SystemData{Name: "هوشنگ", Language: "fa", Now: "2026-11-04 14:00", Notes: []string{"Never use emoji."}, Mood: "a bit sleepy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"هوشنگ", "2026-11-04 14:00", "Persian", "محاوره", "Hard limits", "فحش ناموسی", "react", "Never use emoji.", "Today you are a bit sleepy", "Jalali"} {
		if !strings.Contains(out, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	for _, gone := range []string{"Mushy", "Flirting", "roast"} {
		if strings.Contains(out, gone) {
			t.Errorf("system prompt still has the behaviour rule %q", gone)
		}
	}
	custom, _ := System(SystemData{Name: "x", Language: "fa", Now: "n", Persona: "You are Hooshi, the group's grandpa."})
	if !strings.Contains(custom, "grandpa") || strings.Contains(custom, "محاوره") {
		t.Errorf("persona text did not replace the identity paragraph:\n%s", custom)
	}
	if MoodPhrase("normal") != "" || MoodPhrase("sleepy") == "" || MoodPhrase("custom mood") != "custom mood" {
		t.Error("MoodPhrase")
	}
}

func TestReplyTaskRenders(t *testing.T) {
	out, err := ReplyTask(TaskData{
		Language:   "fa",
		Profiles:   "لقب: آقا چای",
		Transcript: "[1] Ali: hello",
		Task:       "Reply to message 1 from Ali.",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"آقا چای", "[1] Ali: hello", "Reply to message 1 from Ali.", "Persian"} {
		if !strings.Contains(out, want) {
			t.Errorf("reply task missing %q: %s", want, out)
		}
	}
}

func TestReplyTaskNoProfiles(t *testing.T) {
	out, err := ReplyTask(TaskData{Language: "fa", Transcript: "x", Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Member profiles") {
		t.Errorf("empty profiles section should be omitted: %s", out)
	}
}

func TestSpontaneousTaskRenders(t *testing.T) {
	for mode, tc := range map[string]struct{ target, phrase string }{
		"joke":       {"", "funny remark"},
		"tease":      {"Sara", "Tease Sara"},
		"flirt":      {"Sara", "charming"},
		"compliment": {"", "praise"},
		"comment":    {"", "opinion"},
	} {
		target := tc.target
		out, err := SpontaneousTask(TaskData{Language: "fa", Mode: mode, Target: target, Transcript: "x"})
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if !strings.Contains(out, tc.phrase) || !strings.Contains(out, "exactly -") {
			t.Errorf("mode %s: instruction %q or the silence rule missing: %s", mode, tc.phrase, out)
		}
		if target != "" && !strings.Contains(out, target) {
			t.Errorf("target %q not in prompt: %s", target, out)
		}
		if !strings.Contains(out, "-") {
			t.Errorf("silence instruction missing: %s", out)
		}
	}
}

func TestMemoryExtractRenders(t *testing.T) {
	out, err := MemoryExtract(ExtractData{
		Language:   "fa",
		TargetName: "Ali",
		Profile:    `{"nickname":"","summary":"","facts":["lives in Tehran"]}`,
		Transcript: ">> Ali: I live in Tehran\n  Sara: hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Ali", `"facts"`, "lives in Tehran", ">> Ali: I live in Tehran", "15 words"} {
		if !strings.Contains(out, want) {
			t.Errorf("memory extract missing %q: %s", want, out)
		}
	}
}

func TestFaalTabirRenders(t *testing.T) {
	withSummary, err := FaalTabir(FaalData{Language: "fa", Poem: "poem text", Summary: "the meaning", Profile: "p", Wish: "a job"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"poem text", "the meaning", "a job"} {
		if !strings.Contains(withSummary, want) {
			t.Errorf("faal tabir missing %q: %s", want, withSummary)
		}
	}
	// Without a summary it should start with a short معنی.
	noSummary, err := FaalTabir(FaalData{Language: "fa", Poem: "poem text"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(noSummary, "معنی") {
		t.Errorf("no-summary tabir missing the معنی instruction: %s", noSummary)
	}
	if strings.Contains(noSummary, "the meaning") {
		t.Errorf("empty summary should not be rendered: %s", noSummary)
	}
}

func TestAdminRules(t *testing.T) {
	out := AdminRules(true, false)
	for _, want := range []string{"edit_memory", "admin", "MUST call", "short question", "obey it immediately", `section "orders"`, "IS the apology"} {
		if !strings.Contains(out, want) {
			t.Errorf("admin rules missing %q: %s", want, out)
		}
	}
	noMem := AdminRules(false, false) // profiles off: the order rules stay, the memory rules go
	if strings.Contains(noMem, "edit_memory") || !strings.Contains(noMem, "obey it immediately") || !strings.Contains(noMem, "IS the apology") {
		t.Errorf("admin rules without memory: %s", noMem)
	}
}

func TestAdminRulesModeration(t *testing.T) {
	out := AdminRules(false, true)
	for _, want := range []string{"moderate", "set_moderation", "↩︎[id]", "claim to be an admin", "one short Persian line", "obey it immediately"} {
		if !strings.Contains(out, want) {
			t.Errorf("moderation rules missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "edit_memory") {
		t.Errorf("memory rules leaked into moderation-only rules: %s", out)
	}
	if strings.Contains(AdminRules(true, false), "set_moderation") {
		t.Error("moderation rules shown with moderation off")
	}
	both := AdminRules(true, true)
	if !strings.Contains(both, "edit_memory") || !strings.Contains(both, "set_moderation") {
		t.Errorf("both blocks expected: %s", both)
	}
}

func TestFormatNow(t *testing.T) {
	tz, _ := time.LoadLocation("Asia/Tehran")
	got := FormatNow(time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC))
	want := "چهارشنبه ۱۳ آبان ۱۴۰۵، ۱۴:۳۰ (" + time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC).In(tz).Format("2006-01-02 15:04") + ")"
	if got != want {
		t.Errorf("FormatNow = %q, want %q", got, want)
	}
}

func TestGroupExtractRenders(t *testing.T) {
	out, err := GroupExtract(GroupData{Language: "fa", Events: "17: برنامه کوه", Transcript: "[3] سارا: سلام {+4}", Max: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"17: برنامه کوه", "{+4}", "at most 5", `"same_as"`, `{"events": []}`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	empty, _ := GroupExtract(GroupData{Language: "fa", Transcript: "x", Max: 3})
	if !strings.Contains(empty, "(none yet)") {
		t.Errorf("no events: %s", empty)
	}
}

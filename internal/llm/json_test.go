package llm

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	open, close := "\u003cthink\u003e", "\u003c/think\u003e"
	cases := map[string]string{
		open + "  1+1=2  " + close + " 42":      "42",
		open + "a" + close + open + "b" + close: "",
		open + "never closed":                   "",
		"hello":                                 "hello",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractJSONFenced(t *testing.T) {
	in := "```json\n{\"a\": 1}\n```"
	got, err := ExtractJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a": 1}` {
		t.Errorf("got %s", got)
	}
}

func TestExtractJSONPrefixed(t *testing.T) {
	in := "Sure, here you go: {\"a\": {\"b\": 2}}"
	got, err := ExtractJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a": {"b": 2}}` {
		t.Errorf("got %s", got)
	}
}

func TestExtractJSONGarbage(t *testing.T) {
	for _, in := range []string{
		"no json here at all",
		"prefix { unbalanced: }",
	} {
		if _, err := ExtractJSON(in); err == nil {
			t.Errorf("ExtractJSON(%q): want error", in)
		}
	}
}

func TestCleanRemovesThinkBlocks(t *testing.T) {
	open, close := "\u003cthink\u003e", "\u003c/think\u003e"
	in := open + " reasoning " + close + "\n  final answer\n"
	if got := Clean(in); got != "final answer" {
		t.Errorf("Clean = %q, want %q", got, "final answer")
	}
}

func TestBalancedBraceSkipsStrings(t *testing.T) {
	s := `{"a": "}"}`
	if got := balancedBrace(s); got != len(s)-1 {
		t.Errorf("balancedBrace = %d, want %d", got, len(s)-1)
	}
	if got := balancedBrace(`{"a": "}`); got != -1 {
		t.Errorf("unbalanced: got %d, want -1", got)
	}
}

func TestBalancedBraceEscapes(t *testing.T) {
	cases := []string{
		`{"a":"C:\\"}`,
		`{"a":"a\"b"}`,
		`{"a":"{"}`,
		`{"a":"{}"}`,
		`{"a":"\\"}`,
		`{"a":"\\"}`[:len(`{"a":"\\"}`)-1], // unbalanced: unterminated string
	}
	for i, s := range cases {
		want := len(s) - 1
		if i == len(cases)-1 {
			want = -1
		}
		if got := balancedBrace(s); got != want {
			t.Errorf("balancedBrace(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestExtractJSONEscapedBackslash(t *testing.T) {
	in := `{"path":"C:\\","x":1}`
	got, err := ExtractJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != in {
		t.Errorf("got %s, want %s", got, in)
	}
}

func TestFirst300(t *testing.T) {
	if got := first300([]byte(strings.Repeat("a", 400))); len(got) != 300 {
		t.Errorf("len = %d, want 300", len(got))
	}
	if got := first300([]byte("short")); got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestCleanGemmaChannel(t *testing.T) {
	open, close := "\u003c|channel\u003e", "\u003cchannel|\u003e"
	for in, want := range map[string]string{
		open + "thought\n" + close + "اوکی، حله.":                  "اوکی، حله.",
		open + "thought\nlet me think about it\n" + close + "سلام": "سلام",
		open + "thought\nسلام":                                     "سلام",
		close + "سلام":                                             "سلام",
		"سلام":                                                     "سلام",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

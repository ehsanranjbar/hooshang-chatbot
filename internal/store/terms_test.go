package store

import (
	"testing"
	"time"
)

func TestNormalizeTerm(t *testing.T) {
	for in, want := range map[string]string{
		"  قلقلی ":      "قلقلی",
		"كيك‌پز":        "کیک پز",
		"چـایـیدن":      "چاییدن",
		"Hooshi   Mode": "hooshi mode",
		"مَشتی":         "مشتی",
	} {
		if got := NormalizeTerm(in); got != want {
			t.Errorf("NormalizeTerm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGlossaryLRU(t *testing.T) {
	for name, db := range engines(t) {
		t.Run(name, func(t *testing.T) {
			const chat = int64(-5)
			put := func(term, meaning string) *Term {
				t.Helper()
				ev, err := db.PutTerm(chat, Term{Term: term, Meaning: meaning, ByID: 7}, 3)
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(2 * time.Millisecond) // distinct UsedAt
				return ev
			}
			put("الف", "یک")
			put("ب", "دو")
			put("پ", "سه")
			// Using الف makes ب the least recently used.
			if err := db.TouchTerms(chat, []string{"الف"}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * time.Millisecond)
			if ev := put("ت", "چهار"); ev == nil || ev.Term != "ب" {
				t.Fatalf("evicted %+v, want ب", ev)
			}
			// Re-explaining an existing term replaces it without evicting.
			if ev := put("پ", "سه تا"); ev != nil {
				t.Fatalf("an update evicted %+v", ev)
			}
			terms, _ := db.Terms(chat)
			if len(terms) != 3 || terms[0].Term != "پ" || terms[0].Meaning != "سه تا" {
				t.Fatalf("terms = %+v", terms)
			}
			if got, err := db.GetTerm(chat, " پ "); err != nil || got.AddedAt.IsZero() || !got.UsedAt.After(got.AddedAt) {
				t.Fatalf("GetTerm = %+v, %v", got, err)
			}
			if err := db.DeleteTerm(chat, "پ"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.GetTerm(chat, "پ"); err == nil {
				t.Fatal("deleted term still there")
			}
			if other, _ := db.Terms(-6); len(other) != 0 {
				t.Fatal("terms leaked to another chat")
			}
		})
	}
}

package ganjoor

import "testing"

func TestPoetID(t *testing.T) {
	cases := []struct {
		name string
		want int
		ok   bool
	}{
		{"حافظ", 2, true},
		{"خیام", 3, true},
		{"فردوسی", 4, true},
		{"مولوی", 5, true},
		{"مولانا", 5, true},
		{"نظامی", 6, true},
		{"سعدي", 7, true},
		{"پروین", 8, true},
		{"عطار", 9, true},
		{"وحشی", 11, true},
		{"رودکی", 12, true},
		{"باباطاهر", 28, true},
		{"شهریار", 35, true},
		{"ایرج", 88, true},
		// Normalization: ZWNJ, spaces, ي→ی, ك→ک.
		{"ح‌افظ", 2, true},  // with a ZWNJ
		{"حافظ", 2, true},   // ك-variant spelling
		{" خیام ", 3, true}, // surrounding spaces
		{"نظ‌امی", 6, true}, // with a ZWNJ
		{"سعدي", 7, true},   // ي-variant spelling
		{"", 0, false},
		{"شاعرنایاب", 0, false},
	}
	for _, tc := range cases {
		id, ok := PoetID(tc.name)
		if ok != tc.ok || id != tc.want {
			t.Errorf("PoetID(%q) = (%d, %t), want (%d, %t)", tc.name, id, ok, tc.want, tc.ok)
		}
	}
}

func TestPoetNames(t *testing.T) {
	names := PoetNames()
	if len(names) != 13 {
		t.Errorf("PoetNames = %d entries, want 13 (مولوی/مولانا share a poet)", len(names))
	}
}

func TestPoetIDLongerForms(t *testing.T) {
	for in, want := range map[string]int{
		"ایرج میرزا":        88,
		"ایرج‌میرزا":        88,
		"حافظ شیرازی":       2,
		"از سعدی":           7,
		"مولانا جلال‌الدین": 5,
		"رومی":              5,
		"پروین اعتصامی":     8,
		"بابا طاهر":         28,
		"خیام نیشابوری":     3,
	} {
		if got, ok := PoetID(in); !ok || got != want {
			t.Errorf("PoetID(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"شیرازی", "میرزا", "اخوان ثالث", ""} {
		if _, ok := PoetID(in); ok {
			t.Errorf("PoetID(%q) matched", in)
		}
	}
}

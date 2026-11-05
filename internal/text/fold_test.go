package text

import "testing"

func TestFold(t *testing.T) {
	cases := map[string]string{
		"  Hooshang  ":  "hooshang",
		"ايرج‌ميرزا":    "ایرج میرزا",
		"مدرسة":         "مدرسه",
		"کتـــاب":       "کتاب",
		"سَلام\tدوست\n": "سلام دوست",
		"‏چای‎":         "چای",
		"نَبُودَ":       "نبود",
		"یک   دو‌‌سه":   "یک دو سه",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Squash("ایرج میرزا"); got != "ایرجمیرزا" {
		t.Errorf("Squash = %q", got)
	}
	if got := FoldKeepLen("Hooshi كي"); got != "hooshi کی" || len([]rune(got)) != len([]rune("Hooshi كي")) {
		t.Errorf("FoldKeepLen = %q", got)
	}
}

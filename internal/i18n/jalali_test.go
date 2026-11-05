package i18n

import (
	"testing"
	"time"
)

func TestJalali(t *testing.T) {
	cases := []struct {
		g       string
		y, m, d int
	}{
		{"2026-11-05", 1405, 8, 14},
		{"2024-03-20", 1403, 1, 1},
		{"2025-03-21", 1404, 1, 1},
		{"2025-03-20", 1403, 12, 30}, // 1403 is a leap year
		{"2026-03-21", 1405, 1, 1},
		{"2000-01-01", 1378, 10, 11},
		{"2026-09-22", 1405, 6, 31},
		{"2026-09-23", 1405, 7, 1},
	}
	for _, c := range cases {
		tm, _ := time.Parse("2006-01-02", c.g)
		y, m, d := Jalali(tm)
		if y != c.y || m != c.m || d != c.d {
			t.Errorf("Jalali(%s) = %d/%d/%d, want %d/%d/%d", c.g, y, m, d, c.y, c.m, c.d)
		}
	}
}

func TestFormatDate(t *testing.T) {
	tm := time.Date(2026, 11, 5, 18, 5, 0, 0, time.UTC)
	if got := FormatDate(tm); got != "پنجشنبه ۱۴ آبان ۱۴۰۵" {
		t.Errorf("FormatDate = %q", got)
	}
	if got := FormatDateTime(tm); got != "پنجشنبه ۱۴ آبان ۱۴۰۵، ۱۸:۰۵" {
		t.Errorf("FormatDateTime = %q", got)
	}
	if got := When(tm.Add(-30*time.Hour), tm); got != "دیروز" {
		t.Errorf("When yesterday = %q", got)
	}
	if got := When(tm.Add(-5*24*time.Hour), tm); got != "شنبه ۹ آبان ۱۴۰۵" {
		t.Errorf("When 5 days ago = %q", got)
	}
}

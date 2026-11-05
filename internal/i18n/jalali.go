package i18n

import (
	"fmt"
	"time"
)

// Jalali converts a time to the Jalali (Persian) calendar: year, month (1–12)
// and day of the month. The civil algorithm follows jalaali-js (Behrooz
// Kamali), accurate for 1178–3177 AP.
func Jalali(t time.Time) (jy, jm, jd int) {
	return d2j(g2d(t.Year(), int(t.Month()), t.Day()))
}

// jalaliMonths are the month names, 1-based.
var jalaliMonths = [...]string{"", "فروردین", "اردیبهشت", "خرداد", "تیر", "مرداد", "شهریور", "مهر", "آبان", "آذر", "دی", "بهمن", "اسفند"}

// weekdays are the Persian weekday names indexed by time.Weekday.
var weekdays = [...]string{"یکشنبه", "دوشنبه", "سه‌شنبه", "چهارشنبه", "پنجشنبه", "جمعه", "شنبه"}

// MonthFa is the Persian name of a Jalali month (1–12).
func MonthFa(m int) string {
	if m < 1 || m > 12 {
		return ""
	}
	return jalaliMonths[m]
}

// WeekdayFa is the Persian name of a weekday.
func WeekdayFa(d time.Weekday) string { return weekdays[d] }

// FormatDate renders a date in Persian: «چهارشنبه ۱۴ آبان ۱۴۰۵».
func FormatDate(t time.Time) string {
	y, m, d := Jalali(t)
	return WeekdayFa(t.Weekday()) + " " + persianDigits(d) + " " + MonthFa(m) + " " + persianDigits(y)
}

// FormatDateTime renders a date and time in Persian: «چهارشنبه ۱۴ آبان ۱۴۰۵، ۱۸:۰۵».
func FormatDateTime(t time.Time) string {
	return FormatDate(t) + "، " + persianDigits(t.Hour()) + ":" + persianPad2(t.Minute())
}

// When says when something happened, relative when it is recent («امروز»,
// «دیروز»), else the Persian date.
func When(t, now time.Time) string {
	days := int(now.Sub(t).Hours() / 24)
	if days <= 1 {
		return Ago(days)
	}
	return FormatDate(t)
}

// persianPad2 renders n with Persian digits, zero-padded to two.
func persianPad2(n int) string {
	if n < 10 {
		return persianDigits(0) + persianDigits(n)
	}
	return persianDigits(n)
}

// JalaliString renders a Jalali date as "1405/08/14" (Latin digits), for logs.
func JalaliString(t time.Time) string {
	y, m, d := Jalali(t)
	return fmt.Sprintf("%04d/%02d/%02d", y, m, d)
}

// g2d is the Julian day number of a Gregorian date.
func g2d(gy, gm, gd int) int {
	d := div((gy+div(gm-8, 6)+100100)*1461, 4) + div(153*mod(gm+9, 12)+2, 5) + gd - 34840408
	return d - div(div(gy+100100+div(gm-8, 6), 100)*3, 4) + 752
}

// d2j is the Jalali date of a Julian day number.
func d2j(jdn int) (jy, jm, jd int) {
	gy := d2gYear(jdn)
	jy = gy - 621
	leap, _, march := jalCal(jy)
	jdn1f := g2d(gy, 3, march)
	k := jdn - jdn1f
	if k >= 0 {
		if k <= 185 {
			return jy, 1 + k/31, mod(k, 31) + 1
		}
		k -= 186
	} else {
		jy--
		k += 179
		if leap == 1 {
			k++
		}
	}
	return jy, 7 + k/30, mod(k, 30) + 1
}

// d2gYear is the Gregorian year of a Julian day number.
func d2gYear(jdn int) int {
	j := 4*jdn + 139361631
	j += div(div(4*jdn+183187720, 146097)*3, 4)*4 - 3908
	i := div(mod(j, 1461), 4)*5 + 308
	gm := mod(div(i, 153), 12) + 1
	return div(j, 1461) - 100100 + div(8-gm, 6)
}

// jalCal returns, for a Jalali year, the number of years since the last leap
// year (leap == 1 means jy is a leap year), the Gregorian year of its first
// day, and the March day of its first day.
func jalCal(jy int) (leap, gy, march int) {
	breaks := []int{-61, 9, 38, 199, 426, 686, 756, 818, 1111, 1181, 1210, 1635, 2060, 2097, 2192, 2262, 2324, 2394, 2456, 3178}
	gy = jy + 621
	leapJ := -14
	jp := breaks[0]
	jump := 0
	for i := 1; i < len(breaks); i++ {
		jm := breaks[i]
		jump = jm - jp
		if jy < jm {
			break
		}
		leapJ += div(jump, 33)*8 + div(mod(jump, 33), 4)
		jp = jm
	}
	n := jy - jp
	leapJ += div(n, 33)*8 + div(mod(n, 33)+3, 4)
	if mod(jump, 33) == 4 && jump-n == 4 {
		leapJ++
	}
	leapG := div(gy, 4) - div((div(gy, 100)+1)*3, 4) - 150
	march = 20 + leapJ - leapG
	if jump-n < 6 {
		n = n - jump + div(jump+4, 33)*33
	}
	leap = mod(mod(n+1, 33)-1, 4)
	if leap == -1 {
		leap = 4
	}
	return leap, gy, march
}

// div and mod truncate toward zero, as the reference algorithm does.
func div(a, b int) int { return a / b }

func mod(a, b int) int { return a % b }

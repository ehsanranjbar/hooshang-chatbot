// Package i18n holds every fixed Persian string the bot sends. No other
// package may contain Persian string literals (except prompt templates).
package i18n

// Media labels: a media message is shown to the model as "[label: title] caption".
const (
	MediaPhoto     = "عکس"
	MediaSticker   = "استیکر"
	MediaVoice     = "ویس"
	MediaVideo     = "ویدیو"
	MediaVideoNote = "ویدیو مسیج"
	MediaAudio     = "آهنگ"
	MediaFile      = "فایل"
	MediaGIF       = "گیف"
	MediaPoll      = "نظرسنجی"
	MediaLocation  = "لوکیشن"
	MediaVenue     = "مکان"
	MediaContact   = "مخاطب"
	MediaDice      = "تاس"
)

// Help is the reply for /start and /help.
const Help = `سلام! من هوشنگم 🌸
می‌تونی باهام گپ بزنی، فال حافظ بگیری یا ازم شعر بخوای.

دستورها:
/faal [نیت] — فال حافظ
/poem [شاعر] — یه شعر تصادفی
/roast — کباب کردن یکی (روی پیامش ریپلای کن یا منشنش کن)
/terms — اصطلاحات گروه که هوشنگ یاد گرفته
/me — هوشنگ درباره‌ی من چی می‌دونه؟
/notease — دیگه سربه‌سرم نذار
/teaseok — اشکال نداره، سربه‌سرم بذار
/forgetme — هر چی ازم یادته پاک کن
/mute — هوشنگ خودجوش حرف نزنه (ادمین)
/unmute — هوشنگ دوباره خودجوش حرف بزنه (ادمین)
/status — وضعیت ربات (ادمین)
/help — همین راهنما

اصطلاح جدید یادم بده: «هوشنگ، "چاییدن" یعنی سرما خوردن»`

// NotAllowedHere is sent once (and then the bot leaves) in a group where the
// owner is not a member.
const NotAllowedHere = "ببخشید، من فقط توی گروه‌هایی می‌مونم که صاحبم هم باشه. خداحافظ 👋"

// GanjoorDown is sent when the Ganjoor API fails.
const GanjoorDown = "گنجور الان در دسترس نیست، یه کم دیگه دوباره امتحان کن 🙏"

// UnknownPoet is sent when /poem names a poet we don't have.
const UnknownPoet = "این شاعر رو نمی‌شناسم 🤔 اینا رو دارم: حافظ، خیام، فردوسی، مولوی، نظامی، سعدی، پروین، عطار، وحشی، رودکی، باباطاهر، شهریار، ایرج"

// NoTeaseTarget is sent when the roast target has opted out with /notease.
const NoTeaseTarget = "ایشون خواسته سربه‌سرش نذارم، منم رو حرفش هستم 😅"

// RoastWho is sent for /roast with no target.
const RoastWho = "سربه‌سر کی بذارم؟ روی پیامش ریپلای کن یا منشنش کن 🎯"

// NoTerms is sent for /terms when the glossary is empty.
const NoTerms = "هنوز هیچ اصطلاحی یادم ندادین. مثلاً بگین: «هوشنگ، \"چاییدن\" یعنی سرما خوردن»"

// TermsHeader heads the /terms list.
const TermsHeader = "📖 اصطلاحات گروه:"

// FeatureOff is sent when a command's feature is switched off in the config.
const FeatureOff = "این قابلیت الان خاموشه 🙈"

// NothingYet is sent for /me when the member has no profile.
const NothingYet = "هنوز چیز زیادی ازت نمی‌دونم؛ یه کم بیشتر گپ بزنیم 🌱"

// Confirmations of the member and admin commands (§4).
const (
	NoTeaseOn  = "باشه، دیگه سربه‌سرت نمی‌ذارم 🤝"
	NoTeaseOff = "ای جان! پس آماده باش 😏"
	Forgotten  = "پاک شد. انگار اولین باره همدیگه رو می‌بینیم 👋"
	Muted      = "باشه، فقط وقتی صدام کنید حرف می‌زنم 🤐"
	Unmuted    = "برگشتم! 🎉"
)

// RateLimited is the note returned to the LLM when a rate limit is hit
// during a tool call.
const RateLimited = "error: rate limited"

// Faal and poem flow strings.
const (
	FaalHeader   = "🔮 فال حافظ"
	FaalTitle    = "فال حافظ" // the title on the faal image
	MeaningLabel = "📖 معنی:"
	TabirPrefix  = "✨ تعبیر:"
	PoemLink     = "🔗"
)

// Notes returned to the model after faal/poem tool calls.
const (
	FaalSentNote = "sent faal: %s. The poem, its meaning and its interpretation are already in the chat. Do not add anything: reply with exactly -"
	PoemSentNote = "sent poem: %s. The poem is already in the chat. Reply with exactly - unless the user asked you a question about it."
)

// Ago is how long ago an event was last seen, in days: «امروز», «دیروز», «۳ روز پیش».
func Ago(days int) string {
	switch {
	case days <= 0:
		return "امروز"
	case days == 1:
		return "دیروز"
	default:
		return persianDigits(days) + " روز پیش"
	}
}

func persianDigits(n int) string {
	const digits = "۰۱۲۳۴۵۶۷۸۹"
	d := []rune(digits)
	if n == 0 {
		return string(d[0])
	}
	var out []rune
	for ; n > 0; n /= 10 {
		out = append([]rune{d[n%10]}, out...)
	}
	return string(out)
}

// MorningGreetings and NightGreetings are how members greet the group; a
// message containing one (after folding) may get a greeting back.
var (
	MorningGreetings = []string{"صبح بخیر", "صبحتون بخیر", "صبح‌تون بخیر", "صبح همگی بخیر", "صبح به خیر", "روز بخیر", "روزتون بخیر", "ظهر بخیر"}
	NightGreetings   = []string{"شب بخیر", "شبتون بخیر", "شب‌تون بخیر", "شب همگی بخیر", "شب به خیر", "شب خوش", "بخوابیم", "رفتم بخوابم", "خوابم میاد"}
)

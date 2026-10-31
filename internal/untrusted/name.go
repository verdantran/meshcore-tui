package untrusted

import (
	_ "embed"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/norm"
)

// Name is the one form of a name someone gives themselves: NFKC, without invisible characters, with its
// spacing trimmed and collapsed, so "Zed ", "Zed​" and "Ｚｅｄ" are all "Zed". The joiner inside an emoji
// sequence and a flag's tags stay, as Text keeps them: they're part of how the name is drawn and spelt, so
// a channel named "#👩‍🚀crew" keeps the secret other apps derive from it.
func Name(s string) string {
	s = norm.NFKC.String(Text(s))
	s = strings.Map(func(r rune) rune {
		if invisible(r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func invisible(r rune) bool {
	switch {
	case r == zwj, tag(r):
		return false // Text has already dropped any not inside an emoji sequence or flag
	case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Cc, r) && !unicode.IsSpace(r):
		return true
	case r == 0x034f, r == 0x115f, r == 0x1160, r == 0x17b4, r == 0x17b5, r == 0x2800, r == 0x3164, r == 0xffa0:
		return true // default-ignorable or blank but not Cf
	}
	return false
}

func skinTone(r rune) bool { return r >= 0x1f3fb && r <= 0x1f3ff }

// Skeleton is what a name looks like: two names with the same skeleton are hard to tell apart on screen,
// whatever their letters. It folds case, drops accents and maps look-alikes from any script to Latin.
// Capitals are looked up before folding, since "І" looks like "l" where "і" looks like "i". Joiners go too,
// since a terminal that doesn't join emoji draws "👩‍🚀" as "👩🚀", and so do a flag's tags, which take no
// cell: any run of them after 🏴 is kept, and one that spells no flag is drawn as the 🏴 alone. Skin tones
// and a narrow symbol after an emoji, such as the ♂ in "🏊‍♂", go as well, as a terminal that draws joined
// emoji as their parts shows neither, so "🏊‍♂" and "🏊‍♀", or "👍🏽" and "👍🏿", look alike there. Regional
// indicators read as their letters: many terminals draw "🇷" alone as an R, and "🇫🇷" as FR without flags.
func Skeleton(s string) string {
	var b strings.Builder
	afterEmoji := false
	for _, r := range norm.NFKD.String(Name(s)) {
		if unicode.Is(unicode.Mn, r) || r == zwj || tag(r) || skinTone(r) {
			continue
		}
		if r >= 0x1f1e6 && r <= 0x1f1ff {
			r = 'a' + r - 0x1f1e6 // a regional indicator is drawn as its letter where it isn't paired into a flag
		}
		// A narrow symbol such as ♂ after an emoji is left out where joined emoji are drawn as their parts.
		wide := uniseg.StringWidth(string(r)) == 2
		if afterEmoji && !wide && r >= 0x2190 && r <= 0x2bff {
			continue
		}
		afterEmoji = wide && pictographic(r)
		if l, ok := lookalikes[r]; ok {
			r = l
		} else if l, ok := lookalikes[unicode.ToLower(r)]; ok {
			r = l
		} else {
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

//go:embed confusables.txt
var confusables string

// lookalikes maps a character to the Latin letter or digit it reads as: Unicode's confusables, the ASCII
// characters that look like each other, and a few the confusables leave out.
var lookalikes = func() map[rune]rune {
	m := map[rune]rune{}
	for _, line := range strings.Split(confusables, "\n") {
		code, to, ok := strings.Cut(line, " ")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := strconv.ParseUint(code, 16, 32)
		if err != nil || len(to) != 1 {
			panic("untrusted: bad confusables line " + line)
		}
		m[rune(r)] = rune(to[0])
	}
	for r, l := range ownLookalikes {
		m[r] = l
	}
	for r, l := range m {
		// Cherokee small letters are small capitals of the capitals.
		if lr := unicode.ToLower(r); unicode.Is(unicode.Cherokee, r) && lr != r {
			if _, ok := m[lr]; !ok {
				m[lr] = l
			}
		}
	}
	for r, l := range m {
		if a, ok := ownLookalikes[l]; ok && l < utf8.RuneSelf {
			l = a
		}
		m[r] = unicode.ToLower(l)
	}
	return m
}()

var ownLookalikes = map[rune]rune{
	'I': 'l', '1': 'l', '|': 'l', '0': 'o', '5': 's', '8': 'b', '2': 'z', 'І': 'l', 'Ι': 'l', 'Η': 'h', 'Μ': 'm', 'Ν': 'n', 'Υ': 'y',
	// Cyrillic
	'а': 'a', 'в': 'b', 'е': 'e', 'ё': 'e', 'з': '3', 'і': 'i', 'ї': 'i', 'ј': 'j', 'к': 'k', 'м': 'm', 'н': 'h',
	'о': 'o', 'р': 'p', 'с': 'c', 'т': 't', 'у': 'y', 'х': 'x', 'ѕ': 's', 'ԁ': 'd', 'ԛ': 'q', 'ԝ': 'w', 'ӏ': 'l',
	'ь': 'b', 'һ': 'h', 'ү': 'y', 'г': 'r', 'п': 'n',
	// Greek; NFKC turns the lunate sigma ϲ into ς
	'α': 'a', 'β': 'b', 'ε': 'e', 'η': 'n', 'ι': 'i', 'κ': 'k', 'μ': 'u', 'ν': 'v', 'ο': 'o', 'ρ': 'p',
	'τ': 't', 'υ': 'u', 'χ': 'x', 'ζ': 'z', 'γ': 'y', 'ω': 'w', 'ς': 'c',
	// Latin small capitals, which most terminal fonts draw as capitals
	'ᴀ': 'a', 'ʙ': 'b', 'ᴅ': 'd', 'ᴇ': 'e', 'ʜ': 'h', 'ᴊ': 'j', 'ᴋ': 'k', 'ʟ': 'l', 'ᴍ': 'm', 'ɴ': 'n', 'ᴘ': 'p',
	'ʀ': 'r', 'ᴛ': 't', 'ɪ': 'l',
	'Ꮎ': 'o',
}

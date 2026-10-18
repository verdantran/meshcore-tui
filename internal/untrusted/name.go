package untrusted

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Name is the one form of a name someone gives themselves: NFKC, without invisible characters, with its
// spacing trimmed and collapsed, so "Zed ", "Zed​" and "Ｚｅｄ" are all "Zed".
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
	case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Cc, r) && !unicode.IsSpace(r):
		return true
	case r == 0x034f, r == 0x115f, r == 0x1160, r == 0x17b4, r == 0x17b5, r == 0x2800, r == 0x3164, r == 0xffa0:
		return true // default-ignorable or blank but not Cf
	}
	return false
}

// Skeleton is what a name looks like: two names with the same skeleton are hard to tell apart on screen,
// whatever their letters. It folds case, drops accents and maps common Cyrillic and Greek look-alikes to Latin.
// Capitals are looked up before folding, since "І" looks like "l" where "і" looks like "i".
func Skeleton(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(Name(s)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
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

var lookalikes = map[rune]rune{
	'I': 'l', '1': 'l', '|': 'l', '0': 'o', 'І': 'l', 'Ι': 'l', 'Η': 'h', 'Μ': 'm', 'Ν': 'n', 'Υ': 'y',
	// Cyrillic
	'а': 'a', 'в': 'b', 'е': 'e', 'ё': 'e', 'з': '3', 'і': 'i', 'ї': 'i', 'ј': 'j', 'к': 'k', 'м': 'm', 'н': 'h',
	'о': 'o', 'р': 'p', 'с': 'c', 'т': 't', 'у': 'y', 'х': 'x', 'ѕ': 's', 'ԁ': 'd', 'ԛ': 'q', 'ԝ': 'w', 'ӏ': 'l',
	'ь': 'b', 'һ': 'h', 'ү': 'y', 'г': 'r', 'п': 'n',
	// Greek
	'α': 'a', 'β': 'b', 'ε': 'e', 'η': 'n', 'ι': 'i', 'κ': 'k', 'μ': 'u', 'ν': 'v', 'ο': 'o', 'ρ': 'p',
	'τ': 't', 'υ': 'u', 'χ': 'x', 'ζ': 'z', 'γ': 'y', 'ω': 'w',
}

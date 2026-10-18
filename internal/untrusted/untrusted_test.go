package untrusted

import (
	"strings"
	"testing"
	"unicode"
)

const scotland = "\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f"

func TestText(t *testing.T) {
	for in, want := range map[string]string{
		"hello 👋":                                 "hello 👋",
		"two\nlines":                              "two\nlines",
		"tab\there":                               "tab here",
		"\x1bPtmux;x\x1b\\ hi":                    "Ptmux;x\\ hi",
		"\x1b[32m✓ signed\x1b[0m":                 "[32m✓ signed[0m",
		"c1 \u009c st \u0090":                     "c1  st ",
		"evil\u202egnp.exe":                       "evilgnp.exe",
		"bad \xff byte":                           "bad � byte",
		"\x00\x07\x08\r\x7f":                      "",
		"\x1b]8;;https://x\x1b\\link":             "]8;;https://x\\link",
		"Z\u200bed\u200c\u2060\ufeff":             "Zed",
		"\u200eZed\u200f\u061c":                   "Zed",
		"Z\u200ded":                               "Zed",
		"👨\u200d👩\u200d👧\u200d👦":                  "👨\u200d👩\u200d👧\u200d👦",
		"🏳️\u200d🌈":                               "🏳️\u200d🌈",
		"👋\u200d":                                 "👋",
		"a\u200d👋":                                "a👋",
		"Z" + strings.Repeat("\u0301", 30) + "ed": "Z" + strings.Repeat("\u0301", maxMarks) + "ed",
		"para\u2028sep\u2029":                     "para\u2028sep\u2029",
		"so\u00adft\u2061\u2062\u2063\u2064":      "soft",
		"mon\u180egol\ufff9an\ufffano\ufffb":      "mongolanno",
		"tag\U000e0041\U000e0001\U000e007f":       "tag",
		scotland:                                  scotland,
		scotland + "\U000e0067":                   scotland,
		"\U0001f3f4x\U000e0067":                   "\U0001f3f4x",
	} {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLine(t *testing.T) {
	for in, want := range map[string]string{
		"Eve\n10:00:00  SIGNED ✓ Alice": "Eve 10:00:00  SIGNED ✓ Alice",
		"a\u2028b\u2029c\r\nd":          "a b c d",
		"plain":                         "plain",
		"\x1b[2Jtab\tbed":               "[2Jtab bed",
	} {
		if got := Line(in); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzText(f *testing.F) {
	f.Add("\x1b[31mred\u009b")
	f.Add("Z\u200b\u0301\u0301\u0301\u0301\u0301e\n👨\u200d👩")
	f.Fuzz(func(t *testing.T, s string) {
		for _, line := range []bool{false, true} {
			out, marks, prev := clean(s, line), 0, rune(0)
			for _, r := range out {
				if drop(r) || r == '\t' || line && lineBreak(r) || unicode.Is(unicode.Cf, r) && r != zwj && !tag(r) {
					t.Fatalf("%q kept %U", s, r)
				}
				if tag(r) && prev != blackFlag && (!tag(prev) || prev == cancelTag) {
					t.Fatalf("%q kept a stray tag %U", s, r)
				}
				prev = r
				if marks = marks + 1; !unicode.Is(unicode.M, r) {
					marks = 0
				} else if marks > maxMarks {
					t.Fatalf("%q kept %d marks in a row", s, marks)
				}
			}
		}
	})
}

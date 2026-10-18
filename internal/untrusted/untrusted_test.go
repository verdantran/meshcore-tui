package untrusted

import "testing"

func TestText(t *testing.T) {
	for in, want := range map[string]string{
		"hello 👋":                     "hello 👋",
		"two\nlines":                  "two\nlines",
		"tab\there":                   "tab here",
		"\x1bPtmux;x\x1b\\ hi":        "Ptmux;x\\ hi",
		"\x1b[32m✓ signed\x1b[0m":     "[32m✓ signed[0m",
		"c1 \u009c st \u0090":         "c1  st ",
		"evil‮gnp.exe":                "evilgnp.exe",
		"bad \xff byte":               "bad � byte",
		"\x00\x07\x08\r\x7f":          "",
		"\x1b]8;;https://x\x1b\\link": "]8;;https://x\\link",
	} {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzText(f *testing.F) {
	f.Add("\x1b[31mred\u009b")
	f.Fuzz(func(t *testing.T, s string) {
		for _, r := range Text(s) {
			if drop(r) || r == '\t' {
				t.Fatalf("%q kept %U", s, r)
			}
		}
	})
}

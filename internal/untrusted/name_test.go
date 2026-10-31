package untrusted

import (
	"strings"
	"testing"
)

func TestName(t *testing.T) {
	for in, want := range map[string]string{
		"Zed":                             "Zed",
		"Zed ":                            "Zed",
		"  Zed":                           "Zed",
		"Zed\u200b":                       "Zed",
		"Z\u200de\u2060d":                 "Zed",
		"\ufeffZed\u00ad":                 "Zed",
		"\uff3a\uff45\uff44":              "Zed",
		"Zed   the\tGreat":                "Zed the Great",
		"Zed\u3000\u00a0x":                "Zed x",
		"Zed\n10:00 Bob":                  "Zed 10:00 Bob",
		"Zed\u2800\u3164":                 "Zed",
		"Zed" + "          " + "✓ signed": "Zed ✓ signed",
		"Zoë 👋":                           "Zoë 👋",
		"👩\u200d🚀 Ada":                    "👩\u200d🚀 Ada",
		"#👩\u200d🚀crew":                   "#👩\u200d🚀crew",
		"🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f scot": "🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f scot",
		"a\u200d🚀 x":    "a🚀 x",
		"Zed\U000e0067": "Zed",
	} {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkeleton(t *testing.T) {
	zed := Skeleton("Zed")
	for _, s := range []string{"zed", "ZED", "Z\u0435d", "\u0396ed", "Zéd", "Zed ", "Zed\u200b", "\uff3a\uff45\uff44"} {
		if got := Skeleton(s); got != zed {
			t.Errorf("Skeleton(%q) = %q, want %q", s, got, zed)
		}
	}
	if Skeleton("👩\u200d🚀 Ada") != Skeleton("👩🚀 Ada") {
		t.Error("an emoji sequence looks like its emoji side by side")
	}
	scot := "🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f"
	for _, s := range []string{"🏴\U000e0078\U000e007f Bob", "🏴\U000e0078 Bob", scot + " Bob"} {
		if Skeleton(s) != Skeleton("🏴 Bob") {
			t.Errorf("%q, its tags unseen, should look like 🏴 Bob", s)
		}
	}
	if Skeleton("\u0410lice") != Skeleton("alice") || Skeleton("Bob0") != Skeleton("bobo") || Skeleton("\u0406an") != Skeleton("lan") {
		t.Error("Cyrillic and digit look-alikes")
	}
	for _, s := range []string{"Zack", "Ze", "Zed2"} {
		if Skeleton(s) == zed {
			t.Errorf("%q isn't Zed", s)
		}
	}
}

func TestSkeletonLookalikes(t *testing.T) {
	for fake, real := range map[string]string{
		"ᏴᎾᏴ":          "BOB",
		"ᎪᏴᏟᎬᎻᎫᏦᏞᎷᏢᏚᏔ": "ABCEHJKLMPSW",
		"ꮯꮪꮩꮃ":         "CSVW",
		"օսհոցզ":       "ouhngq",
		"ıȷɑɡɩǀ":       "ijagil",
		"ᴀʙᴄᴅᴇʜɪᴊᴋʟᴍɴᴏᴘʀꜱᴛᴜᴠᴡʏᴢ": "ABCDEHIJKLMNOPRSTUVWYZ",
		"ϲϳѵ":          "cjv",
		"a‐b‑c−d–e":    "a-b-c-d-e",
		"5ALT 8OB 2ED": "SALT BOB ZED",
	} {
		if Skeleton(fake) != Skeleton(real) {
			t.Errorf("Skeleton(%q) = %q, Skeleton(%q) = %q", fake, Skeleton(fake), real, Skeleton(real))
		}
	}
	for _, s := range []string{"Bob", "Alice", "Zed", "x-y"} {
		if sk := Skeleton(s); sk != strings.ToLower(sk) || len(sk) != len(s) {
			t.Errorf("Skeleton(%q) = %q", s, sk)
		}
	}
}

func TestSkeletonMatchesSplitEmoji(t *testing.T) {
	// Where joined emoji are drawn as their parts, the narrow symbol and the skin tone don't show.
	for _, same := range [][]string{
		{"🏊‍♂ Bob", "🏊‍♀ Bob", "🏊 Bob", "🏊♂ Bob"},
		{"👍🏽 Bob", "👍🏿 Bob", "👍 Bob"},
		{"🧑‍⚕️ Ann", "🧑 Ann"},
	} {
		for _, s := range same[1:] {
			if Skeleton(s) != Skeleton(same[0]) {
				t.Errorf("%q should look like %q", s, same[0])
			}
		}
	}
	for _, s := range []string{"Bob ↑", "Bob ↑↓", "👩‍🚀 Ada"} {
		if Skeleton(s) == Skeleton("Bob") || Skeleton("👩‍🚀 Ada") == Skeleton("👩 Ada") {
			t.Errorf("%q: only narrow symbols after an emoji go", s)
		}
	}
	if Skeleton("Bob ↑") == Skeleton("Bob ↑↓") {
		t.Error("arrows after text aren't emoji parts")
	}
	if Name("🏊‍♂ Bob") != "🏊‍♂ Bob" || Name("👍🏽 Bob") != "👍🏽 Bob" {
		t.Error("Name keeps every character")
	}
}

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

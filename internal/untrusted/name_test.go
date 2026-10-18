package untrusted

import "testing"

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

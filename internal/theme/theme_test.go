package theme

import (
	"math"
	"testing"
)

func luminance(c RGB) float64 {
	ch := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

// contrast is the WCAG contrast ratio between two colours.
func contrast(a, b RGB) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func distance(a, b RGB) float64 {
	dr, dg, db := float64(a.R)-float64(b.R), float64(a.G)-float64(b.G), float64(a.B)-float64(b.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func TestThemesStayReadable(t *testing.T) {
	for _, th := range All {
		if th.Mono {
			continue
		}
		checks := []struct {
			name   string
			fg, bg RGB
			min    float64
		}{
			{"text on bg", th.Text, th.Bg, 7},
			{"muted on bg", th.Muted, th.Bg, 4.5},
			{"muted on panel", th.Muted, th.Panel, 4.5},
			{"dim on bg", th.Dim, th.Bg, 3},
			{"text on raised", th.Text, th.Raised, 4.5},
			{"magenta on bg", th.Magenta, th.Bg, 3},
			{"cyan on bg", th.Cyan, th.Bg, 3},
			{"cyan on panel", th.Cyan, th.Panel, 3},
			{"violet on bg", th.Violet, th.Bg, 3},
			{"green on bg", th.Green, th.Bg, 3},
			{"yellow on bg", th.Yellow, th.Bg, 3},
			{"red on bg", th.Red, th.Bg, 3},
			{"bg on magenta (header tag)", th.Bg, th.Magenta, 4.5},
			{"text on tinted magenta (@you)", th.Text, th.Tint(th.Magenta), 4.5},
			{"text on tinted violet (someone else's @mention)", th.Text, th.Tint(th.Violet), 4.5},
		}
		for _, c := range checks {
			if r := contrast(c.fg, c.bg); r < c.min {
				t.Errorf("%s: %s contrast %.2f, want ≥ %.1f", th.Name, c.name, r, c.min)
			}
		}
		// The signal scale must read as three different states.
		scale := []RGB{th.Green, th.Yellow, th.Red}
		for i := range scale {
			for j := i + 1; j < len(scale); j++ {
				if d := distance(scale[i], scale[j]); d < 90 {
					t.Errorf("%s: signal colours %d and %d too close (%.0f)", th.Name, i, j, d)
				}
			}
		}
	}
}

func TestCycleVisitsEveryTheme(t *testing.T) {
	seen := map[string]bool{}
	th := Neon
	for range All {
		seen[th.Name] = true
		th = th.Next()
	}
	if len(seen) != len(All) || th.Name != Neon.Name {
		t.Fatalf("cycle saw %v and ended on %s", seen, th.Name)
	}
}

func TestBlendEnds(t *testing.T) {
	if Blend(Neon, Ice, 0).Bg != Neon.Bg || Blend(Neon, Ice, 1).Bg != Ice.Bg {
		t.Fatal("blend should start on the old theme and end on the new one")
	}
}

func TestLookupsAndStyles(t *testing.T) {
	if ByName("ice").Name != "ice" || ByName("nope").Name != "neon" || (Theme{Name: "nope"}).Next().Name != "neon" {
		t.Error("lookups fall back to neon")
	}
	if len(Names()) != len(All) {
		t.Error("names")
	}
	t.Setenv("NO_COLOR", "")
	if Default("dusk").Name != "dusk" {
		t.Error("default by name")
	}
	t.Setenv("NO_COLOR", "1")
	if Default("dusk").Name != "mono" {
		t.Error("NO_COLOR forces mono")
	}
	if Hex("#ff2ec4") != (RGB{0xff, 0x2e, 0xc4}) || Neon.Magenta.Color() == nil {
		t.Error("colours")
	}
	plain := Mono.Fg(Mono.Text, true).Render("x")
	coloured := Neon.Fg(Neon.Text, false).Render("x")
	if plain == coloured || Mono.Block(Mono.Bg, Mono.Magenta).Render("x") == Neon.Block(Neon.Bg, Neon.Magenta).Render("x") {
		t.Error("mono should drop colour")
	}
	if Neon.SNR(5) != Neon.Green || Neon.SNR(0) != Neon.Yellow || Neon.SNR(-0.25) != Neon.Red {
		t.Error("signal scale")
	}
	if Blend(Mono, Neon, 0.2).Name != "mono" || Blend(Mono, Neon, 0.8).Name != "neon" {
		t.Error("mono switches at the midpoint")
	}
}

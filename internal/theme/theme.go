// Package theme holds the colour tokens and shared styles.
package theme

import (
	"fmt"
	"image/color"
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// RGB is a token colour that can be blended for fades.
type RGB struct{ R, G, B uint8 }

func Hex(s string) RGB {
	var c RGB
	fmt.Sscanf(s, "#%02x%02x%02x", &c.R, &c.G, &c.B)
	return c
}

func (c RGB) Lerp(o RGB, t float64) RGB {
	t = min(max(t, 0), 1)
	f := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t + 0.5) }
	return RGB{f(c.R, o.R), f(c.G, o.G), f(c.B, o.B)}
}

func (c RGB) Color() color.Color { return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)) }

type Theme struct {
	Name string
	Mono bool

	Bg, Panel, Raised, Line RGB
	Text, Muted, Dim        RGB
	Magenta, Cyan, Violet   RGB
	Green, Yellow, Red      RGB
}

var Neon = Theme{
	Name:    "neon",
	Bg:      Hex("#0d0717"),
	Panel:   Hex("#160c26"),
	Raised:  Hex("#24123d"),
	Line:    Hex("#3b2560"),
	Text:    Hex("#ece6ff"),
	Muted:   Hex("#9184b8"),
	Dim:     Hex("#6a5d90"),
	Magenta: Hex("#ff2ec4"),
	Cyan:    Hex("#23e5ff"),
	Violet:  Hex("#a26bff"),
	Green:   Hex("#3dff9e"),
	Yellow:  Hex("#ffd23f"),
	Red:     Hex("#ff3d6b"),
}

// Dusk: deep plum, pink focus, orange for live data.
var Dusk = Theme{
	Name:    "dusk",
	Bg:      Hex("#140810"),
	Panel:   Hex("#1f0d18"),
	Raised:  Hex("#331427"),
	Line:    Hex("#5a2340"),
	Text:    Hex("#ffe8f0"),
	Muted:   Hex("#c08aa8"),
	Dim:     Hex("#8a5672"),
	Magenta: Hex("#ff5c9d"),
	Cyan:    Hex("#ff9a52"),
	Violet:  Hex("#c78bff"),
	Green:   Hex("#7dffb0"),
	Yellow:  Hex("#fff06b"),
	Red:     Hex("#ff4f5e"),
}

// Ice: navy-black, electric blue focus, pale cyan for live data.
var Ice = Theme{
	Name:    "ice",
	Bg:      Hex("#050b12"),
	Panel:   Hex("#0a1420"),
	Raised:  Hex("#12233a"),
	Line:    Hex("#1f3a5c"),
	Text:    Hex("#e6f4ff"),
	Muted:   Hex("#8aa8c8"),
	Dim:     Hex("#4f6c8c"),
	Magenta: Hex("#4fb0ff"),
	Cyan:    Hex("#9ef0ff"),
	Violet:  Hex("#b8a6ff"),
	Green:   Hex("#5effc0"),
	Yellow:  Hex("#ffe066"),
	Red:     Hex("#ff5c7a"),
}

// Amber: amber phosphor; state colours stay warm but distinct.
var Amber = Theme{
	Name:    "amber",
	Bg:      Hex("#0c0802"),
	Panel:   Hex("#170f04"),
	Raised:  Hex("#2a1c06"),
	Line:    Hex("#4a3210"),
	Text:    Hex("#ffd28a"),
	Muted:   Hex("#c08a3a"),
	Dim:     Hex("#86602a"),
	Magenta: Hex("#ffb000"),
	Cyan:    Hex("#ffe4a8"),
	Violet:  Hex("#e8945a"),
	Green:   Hex("#7fe85a"),
	Yellow:  Hex("#ffd23f"),
	Red:     Hex("#ff5a3c"),
}

// Phosphor: green phosphor; good signal is lime and marginal is orange, so neither vanishes into the green.
var Phosphor = Theme{
	Name:    "phosphor",
	Bg:      Hex("#020a04"),
	Panel:   Hex("#06140a"),
	Raised:  Hex("#0c2414"),
	Line:    Hex("#1a4026"),
	Text:    Hex("#c4ffd2"),
	Muted:   Hex("#5fbf7a"),
	Dim:     Hex("#3a7a4e"),
	Magenta: Hex("#39ff6a"),
	Cyan:    Hex("#a8ffe0"),
	Violet:  Hex("#7fe0a0"),
	Green:   Hex("#d4ff4a"),
	Yellow:  Hex("#ffa630"),
	Red:     Hex("#ff4a4a"),
}

// Light is for bright rooms; accents are darkened to keep contrast on a pale ground.
var Light = Theme{
	Name:    "light",
	Bg:      Hex("#f5f2fa"),
	Panel:   Hex("#e9e3f4"),
	Raised:  Hex("#dbd0ef"),
	Line:    Hex("#b4a4d2"),
	Text:    Hex("#1d1430"),
	Muted:   Hex("#54476e"),
	Dim:     Hex("#6f6489"),
	Magenta: Hex("#b80d82"),
	Cyan:    Hex("#006f8e"),
	Violet:  Hex("#5f36c4"),
	Green:   Hex("#0a7a40"),
	Yellow:  Hex("#8f5a00"),
	Red:     Hex("#c0213a"),
}

// Vapor: deep violet, pastel pink focus, aqua for live data.
var Vapor = Theme{
	Name:    "vapor",
	Bg:      Hex("#120a1f"),
	Panel:   Hex("#1b1030"),
	Raised:  Hex("#2a1a48"),
	Line:    Hex("#45306e"),
	Text:    Hex("#fdf0ff"),
	Muted:   Hex("#b39ad6"),
	Dim:     Hex("#7e68a6"),
	Magenta: Hex("#ff71ce"),
	Cyan:    Hex("#01cdfe"),
	Violet:  Hex("#b967ff"),
	Green:   Hex("#05ffa1"),
	Yellow:  Hex("#fffb96"),
	Red:     Hex("#ff4f7b"),
}

// Midnight: navy, gold focus, sky blue for live data.
var Midnight = Theme{
	Name:    "midnight",
	Bg:      Hex("#070a1a"),
	Panel:   Hex("#0d1228"),
	Raised:  Hex("#18203f"),
	Line:    Hex("#2b3766"),
	Text:    Hex("#eef1ff"),
	Muted:   Hex("#9aa5d1"),
	Dim:     Hex("#64709c"),
	Magenta: Hex("#ffcc33"),
	Cyan:    Hex("#6ee7ff"),
	Violet:  Hex("#9fa8ff"),
	Green:   Hex("#4dffa6"),
	Yellow:  Hex("#ff9f43"),
	Red:     Hex("#ff4d6d"),
}

// Ember: charred black, flame orange focus, gold for live data.
var Ember = Theme{
	Name:    "ember",
	Bg:      Hex("#0e0605"),
	Panel:   Hex("#190b08"),
	Raised:  Hex("#2c140e"),
	Line:    Hex("#52261a"),
	Text:    Hex("#fff0e6"),
	Muted:   Hex("#d09a80"),
	Dim:     Hex("#946450"),
	Magenta: Hex("#ff6a2b"),
	Cyan:    Hex("#ffc53d"),
	Violet:  Hex("#ff8f8f"),
	Green:   Hex("#a6f06a"),
	Yellow:  Hex("#ffe14d"),
	Red:     Hex("#ff3355"),
}

// Toxic: black, acid lime focus, teal for live data, violet to set them off.
var Toxic = Theme{
	Name:    "toxic",
	Bg:      Hex("#050903"),
	Panel:   Hex("#0b1207"),
	Raised:  Hex("#16230e"),
	Line:    Hex("#2e4a1c"),
	Text:    Hex("#efffe0"),
	Muted:   Hex("#9cc27e"),
	Dim:     Hex("#678a4e"),
	Magenta: Hex("#c6ff00"),
	Cyan:    Hex("#00ffd0"),
	Violet:  Hex("#c070ff"),
	Green:   Hex("#4dff7a"),
	Yellow:  Hex("#ffb300"),
	Red:     Hex("#ff3860"),
}

// Paper: warm cream for long reading in daylight; rust focus, teal for live data.
var Paper = Theme{
	Name:    "paper",
	Bg:      Hex("#f7f1e3"),
	Panel:   Hex("#efe6d2"),
	Raised:  Hex("#e3d6ba"),
	Line:    Hex("#c4b28c"),
	Text:    Hex("#2b2014"),
	Muted:   Hex("#5c4a33"),
	Dim:     Hex("#76624b"),
	Magenta: Hex("#a3361f"),
	Cyan:    Hex("#1f6b75"),
	Violet:  Hex("#6a3f9e"),
	Green:   Hex("#2e7d32"),
	Yellow:  Hex("#8a5a00"),
	Red:     Hex("#c0103e"),
}

var HighContrast = func() Theme {
	t := Neon
	t.Name = "high-contrast"
	t.Bg, t.Panel, t.Raised, t.Line = Hex("#000000"), Hex("#000000"), Hex("#1e1e1e"), Hex("#8a8a8a")
	t.Text, t.Muted, t.Dim = Hex("#ffffff"), Hex("#d8d8d8"), Hex("#a8a8a8")
	return t
}()

var Mono = func() Theme {
	t := HighContrast
	t.Name, t.Mono = "mono", true
	return t
}()

// All is the order themes cycle in.
var All = []Theme{Neon, Dusk, Vapor, Ice, Midnight, Ember, Amber, Toxic, Phosphor, Light, Paper, HighContrast, Mono}

func ByName(name string) Theme {
	for _, t := range All {
		if t.Name == name {
			return t
		}
	}
	return Neon
}

func Names() []string {
	out := make([]string, len(All))
	for i, t := range All {
		out[i] = t.Name
	}
	return out
}

// Next is the theme after this one in the cycle.
func (t Theme) Next() Theme {
	for i, o := range All {
		if o.Name == t.Name {
			return All[(i+1)%len(All)]
		}
	}
	return Neon
}

// Tint is an accent faded most of the way into the background, for highlights that shouldn't shout.
func (t Theme) Tint(c RGB) RGB { return c.Lerp(t.Bg, 0.6) }

// Blend crossfades every token from a to b. Mono has no colours to blend, so it switches at the midpoint.
func Blend(a, b Theme, t float64) Theme {
	if a.Mono || b.Mono {
		if t < 0.5 {
			return a
		}
		return b
	}
	out := b
	pairs := []struct {
		dst  *RGB
		x, y RGB
	}{
		{&out.Bg, a.Bg, b.Bg}, {&out.Panel, a.Panel, b.Panel}, {&out.Raised, a.Raised, b.Raised}, {&out.Line, a.Line, b.Line},
		{&out.Text, a.Text, b.Text}, {&out.Muted, a.Muted, b.Muted}, {&out.Dim, a.Dim, b.Dim},
		{&out.Magenta, a.Magenta, b.Magenta}, {&out.Cyan, a.Cyan, b.Cyan}, {&out.Violet, a.Violet, b.Violet},
		{&out.Green, a.Green, b.Green}, {&out.Yellow, a.Yellow, b.Yellow}, {&out.Red, a.Red, b.Red},
	}
	for _, p := range pairs {
		*p.dst = p.x.Lerp(p.y, t)
	}
	return out
}

// Default honours NO_COLOR before the configured name.
func Default(name string) Theme {
	if os.Getenv("NO_COLOR") != "" {
		return Mono
	}
	return ByName(name)
}

// Fg styles text in a token colour; bold stands in for glow on accents.
func (t Theme) Fg(c RGB, bold bool) lipgloss.Style {
	s := lipgloss.NewStyle().Bold(bold)
	if t.Mono {
		return s
	}
	return s.Foreground(c.Color())
}

// Paint is Fg(c, bold).Render(s) without building a style, for drawing loops; the output is the same bytes.
func (t Theme) Paint(c RGB, bold bool, s string) string {
	if strings.ContainsAny(s, "\t\r\n") {
		return t.Fg(c, bold).Render(s)
	}
	if t.Mono && !bold {
		return s
	}
	b := make([]byte, 0, len(s)+24)
	b = t.sgr(b, c, bold)
	b = append(b, s...)
	b = append(b, "\x1b[m"...)
	return string(b)
}

// sgr appends the escape that opens Fg(c, bold), which must be one that sets something.
func (t Theme) sgr(b []byte, c RGB, bold bool) []byte {
	b = append(b, "\x1b["...)
	if bold {
		b = append(b, '1')
		if t.Mono {
			return append(b, 'm')
		}
		b = append(b, ';')
	}
	b = append(b, "38;2;"...)
	b = strconv.AppendUint(b, uint64(c.R), 10)
	b = append(b, ';')
	b = strconv.AppendUint(b, uint64(c.G), 10)
	b = append(b, ';')
	b = strconv.AppendUint(b, uint64(c.B), 10)
	return append(b, 'm')
}

// Block is solid colour behind text, used for the header's device tag and badges.
func (t Theme) Block(fg, bg RGB) lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true)
	if t.Mono {
		return s.Reverse(true)
	}
	return s.Foreground(fg.Color()).Background(bg.Color())
}

// SNR colours a signal value on the shared green/yellow/red scale.
func (t Theme) SNR(db float64) RGB {
	switch {
	case db >= 5:
		return t.Green
	case db >= 0:
		return t.Yellow
	}
	return t.Red
}

package mapview

import "meshcore-tui/internal/theme"

// Palette is the map's colours, worked out from a theme so every theme gets a matching map.
type Palette struct {
	Mono bool

	Sea, Land, Coast, River, Border, Town   theme.RGB
	Motorway, Trunk, Primary                theme.RGB
	Self, Repeater, Chat, Room, Faded       theme.RGB
	Text, Dim, Cyan, Magenta, Violet, Green theme.RGB
	Unknown, AltRoute, Bg                   theme.RGB

	snr func(float64) theme.RGB
}

func luminance(c theme.RGB) float64 {
	return (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 255
}

func PaletteFor(th theme.Theme) Palette {
	black, white := theme.RGB{}, theme.RGB{R: 255, G: 255, B: 255}
	light := luminance(th.Bg) > 0.5
	land := th.Bg.Lerp(th.Panel, 0.7)
	sea := th.Bg.Lerp(th.Cyan, 0.05).Lerp(black, 0.4)
	if light {
		land = th.Bg.Lerp(th.Panel, 0.5)
		sea = th.Bg.Lerp(th.Cyan, 0.16).Lerp(white, 0.2)
	}
	return Palette{
		Mono:     th.Mono,
		Sea:      sea,
		Land:     land,
		Coast:    th.Cyan.Lerp(land, 0.2),
		River:    th.Violet.Lerp(th.Cyan, 0.35).Lerp(land, 0.45),
		Border:   th.Line.Lerp(land, 0.25),
		Town:     th.Muted.Lerp(land, 0.3),
		Motorway: th.Magenta.Lerp(land, 0.58),
		Trunk:    th.Magenta.Lerp(th.Violet, 0.5).Lerp(land, 0.72),
		Primary:  th.Violet.Lerp(land, 0.75),
		Self:     th.Text,
		Repeater: th.Text.Lerp(th.Violet, 0.1),
		Chat:     th.Cyan,
		Room:     th.Violet,
		Faded:    th.Dim,
		Text:     th.Text,
		Dim:      th.Dim,
		Cyan:     th.Cyan,
		Magenta:  th.Magenta,
		Violet:   th.Violet,
		Green:    th.Green,
		Unknown:  th.Muted.Lerp(land, 0.35),
		AltRoute: th.Violet.Lerp(th.Text, 0.5),
		Bg:       th.Bg,
		snr:      th.SNR,
	}
}

// SNR colours a measured link the way the rest of the app colours signal.
func (p Palette) SNR(db float64) theme.RGB { return p.snr(db) }

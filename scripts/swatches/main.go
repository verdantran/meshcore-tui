// Command swatches draws each theme's colour tokens as an SVG card for the README: go run ./scripts/swatches.
package main

import (
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/verdantran/meshcore-tui/internal/theme"
)

const (
	cell   = 52 // each swatch's slot
	square = 40
	pad    = 20
)

type token struct {
	name string
	c    theme.RGB
}

func hex(c theme.RGB) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

func card(t theme.Theme) string {
	tokens := []token{
		{"bg", t.Bg}, {"panel", t.Panel}, {"raised", t.Raised}, {"line", t.Line},
		{"text", t.Text}, {"muted", t.Muted}, {"dim", t.Dim},
		{"magenta", t.Magenta}, {"cyan", t.Cyan}, {"violet", t.Violet},
		{"green", t.Green}, {"yellow", t.Yellow}, {"red", t.Red},
	}
	note := ""
	if t.Mono {
		// mono draws no colour of its own: the terminal's text on its background, set apart by bold and lines.
		tokens = []token{{"bg", theme.Hex("#000000")}, {"text", theme.Hex("#ffffff")}}
		note = "no colour: bold, underline and line weight"
	}
	w := pad*2 + cell*13
	h := 104
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`+"\n", w, h, w, h)
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="10" fill="%s" stroke="%s"/>`+"\n", w-1, h-1, hex(t.Bg), hex(t.Line))
	font := `font-family="ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"`
	fmt.Fprintf(&b, `<text x="%d" y="30" %s font-size="15" font-weight="bold" fill="%s">%s</text>`+"\n", pad, font, hex(t.Text), html.EscapeString(t.Name))
	if note != "" {
		fmt.Fprintf(&b, `<text x="%d" y="30" %s font-size="12" fill="%s">%s</text>`+"\n", pad+150, font, hex(t.Muted), note)
	}
	for i, tok := range tokens {
		x := pad + i*cell
		fmt.Fprintf(&b, `<rect x="%d" y="44" width="%d" height="%d" rx="6" fill="%s" stroke="%s"><title>%s %s</title></rect>`+"\n",
			x, square, square, hex(tok.c), hex(t.Line), tok.name, hex(tok.c))
		fmt.Fprintf(&b, `<text x="%d" y="98" %s font-size="10" fill="%s">%s</text>`+"\n", x, font, hex(t.Muted), tok.name)
	}
	b.WriteString("</svg>\n")
	return b.String()
}

func main() {
	dir := filepath.Join("docs", "themes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, t := range theme.All {
		if err := os.WriteFile(filepath.Join(dir, t.Name+".svg"), []byte(card(t)), 0o644); err != nil {
			log.Fatal(err)
		}
	}
}

package console

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

func TestSuggest(t *testing.T) {
	rep, comp := Node(radio.TypeRepeater), Companion()
	for _, c := range []struct {
		cmds             []Command
		in, ghost, args  string
		matches, helpHas string
	}{
		{comp, "get rad", "io", "", "get radio · get radio.rxgain · get radio.fem.rxgain", ""},
		{comp, "get radio.f", "em.rxgain", "", "", "front-end module"},
		{comp, "set radio.fem.rxgain", "", "on|off", "", "receive amplifier"},
		{comp, "set radio.fem.rxgain ", "", "on|off", "set radio.fem.rxgain on · set radio.fem.rxgain off", "receive amplifier"},
		{comp, "set radio.fem.rxgain o", "", "", "set radio.fem.rxgain on · set radio.fem.rxgain off", ""},
		{comp, "set radio.fem.rxgain of", "f", "", "", ""},
		{comp, "set tx ", "", "<dBm>", "", "Transmit power"},
		{comp, "set tx 2", "", "", "", "Transmit power"},
		{comp, "SET TX", "", "<dBm>", "set tx · set txdelay", ""},
		{comp, "zz", "", "", "", ""},
		{comp, "", "", "", "", ""},
		{rep, "get flood.ma", "x", "", "get flood.max · get flood.max.unscoped · get flood.max.advert", ""},
		{rep, "set repeat ", "", "on|off", "set repeat on · set repeat off", "Repeat packets"},
		{rep, "reg", "ion", "", "region · region home · region put · region remove · region allowf · region denyf · region get · region default · region list · region save", ""},
		{rep, "powersaving", "", "[on|off]", "", "power saving"},
	} {
		h := Suggest(c.cmds, c.in)
		if h.Ghost != c.ghost || h.Args != c.args || strings.Join(h.Matches, " · ") != c.matches || !strings.Contains(h.Help, c.helpHas) {
			t.Errorf("%q: ghost %q args %q matches %q help %q", c.in, h.Ghost, h.Args, strings.Join(h.Matches, " · "), h.Help)
		}
	}
}

func TestRoles(t *testing.T) {
	has := func(cmds []Command, w string) bool {
		return slices.ContainsFunc(cmds, func(c Command) bool { return c.Words == w })
	}
	if !has(Node(radio.TypeRepeater), "neighbors") || has(Node(radio.TypeRoom), "neighbors") {
		t.Error("neighbours are a repeater's")
	}
	if has(Node(radio.TypeRepeater), "set radio.fem.rxgain") == has(Companion(), "set radio.fem.rxgain") && !has(Companion(), "set radio.fem.rxgain") {
		t.Error("the companion has its FEM gain")
	}
	for _, cmds := range [][]Command{Companion(), Node(radio.TypeRepeater), Node(radio.TypeRoom)} {
		for _, c := range cmds {
			if c.Words == "" || c.Help == "" || c.Group == "" || len(c.Values) > 0 && c.Args == "" {
				t.Errorf("%+v is missing something", c)
			}
		}
	}
}

func TestDocIsCurrent(t *testing.T) {
	doc, err := os.ReadFile("../../docs/console.md")
	if err != nil || string(doc) != Markdown() {
		t.Fatal("docs/console.md is out of date with the command tables: run go run ./scripts/consoledoc")
	}
}

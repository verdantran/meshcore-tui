package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

const hostile = "\x1bP+q544e\x1b\\ \x1b_Ga=q;AAAA\x1b\\ \x1b]52;c;cHduZWQ=\x07 \x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\ \x1b[32m✓ signed\x1b[0m \u009d0;title\u009c \x1b[2J"

// terminalBytes is what the renderer would write for the app's current frame.
func terminalBytes(h *harness) string {
	sb := uv.NewScreenBuffer(140, 44)
	uv.NewStyledString(h.m.View().Content).Draw(sb, sb.Bounds())
	var out bytes.Buffer
	r := uv.NewTerminalRenderer(&out, nil)
	r.Render(sb.RenderBuffer)
	r.Flush()
	return out.String()
}

func TestTextFromTheMeshCantDriveTheTerminal(t *testing.T) {
	h := newHarness(t)
	h.openConvo("Public")
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 1, Text: "Mallory: hi " + hostile})
	h.m.Update(eventMsg{radio.Heard{Contact: radio.Contact{Key: [32]byte{0xEE}, Name: "\x1bP+q\x1b\\Eve", Type: radio.TypeChat}}})
	h.advance(3 * time.Second)

	last := h.m.chats.msgs[len(h.m.chats.msgs)-1]
	if strings.ContainsAny(last.Text, "\x1b\u009d\u009c") {
		t.Errorf("the message keeps its control characters: %q", last.Text)
	}
	if strings.Contains(strings.Join(h.m.messageLines(last, 200), ""), "\x1b[32m✓") {
		t.Error("a message can't colour its own text")
	}
	out := terminalBytes(h)
	for name, seq := range map[string]string{"DCS": "\x1bP", "APC": "\x1b_", "OSC": "\x1b]", "8-bit OSC": "\u009d", "erase": "\x1b[2J"} {
		if strings.Contains(out, seq) {
			t.Errorf("%s from the mesh reaches the terminal", name)
		}
	}
	if !strings.Contains(out, "click") || !strings.Contains(out, "signed") {
		t.Error("the text around them still shows")
	}
}

func TestDrawableKeepsOnlyTextAndColour(t *testing.T) {
	plain := "\x1b[1;38;2;1;2;3mbold\x1b[m and plain\nnext"
	if drawable(plain) != plain {
		t.Error("a frame of text and colour is left alone")
	}
	if got := drawable("a\x1bPq\x1b\\b\x1b[31mc\x1b]0;x\x07d\u009dz\u009ce"); got != "ab\x1b[31mcdze" {
		t.Errorf("drawable = %q", got)
	}
}

func TestExportsStayInTheirFolder(t *testing.T) {
	for in, want := range map[string]string{"../../.ssh/x": "_______ssh_x", "bristol": "bristol", "café_crew": "café_crew", "..": "conversation", `a\b:c`: "a_b_c"} {
		if got := fileName(in); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
	h := newHarness(t)
	f, path, err := h.m.createExport(fileName(slug("../../evil")) + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if filepath.Dir(path) != h.m.opt.ExportDir {
		t.Errorf("exported to %s", path)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("an export is %v; only you should read it", st.Mode().Perm())
	}
}

func TestSurprisesFromTheRadioAreSkipped(t *testing.T) {
	h := newHarness(t)
	if cmd := h.m.safely("a test packet", func() tea.Cmd { panic("unexpected") }); cmd != nil || !strings.Contains(h.lastLog(), "SKIPPED  a test packet that couldn't be read") {
		t.Errorf("a panic is logged and skipped: %s", h.lastLog())
	}
}

func FuzzEmphasis(f *testing.F) {
	f.Add("*a* _b_ ~c~ `d` **e** @[x_y] 2*3*4")
	f.Fuzz(func(t *testing.T, s string) {
		clean, marks := emphasis(s)
		if marks != nil && len(marks) != len(clean) {
			t.Fatalf("%d marks for %d bytes", len(marks), len(clean))
		}
		wrapEmph(clean, marks, 7, func(s string, w int) string { return s })
		shortcodeAt(s)
	})
}

func FuzzDrawable(f *testing.F) {
	f.Add("a\x1bPq\x1b\\b\x1b[31mc\x1b]0;x\x07d")
	f.Add("a\u009b2Jb\u0085c\x90+q544e\x1b\\d\r\x1b[2Kfake\x7f")
	f.Fuzz(func(t *testing.T, s string) {
		for _, in := range []string{s, s + "\x1b]"} {
			if bad := undrawable(drawable(in)); bad != "" {
				t.Fatalf("%q kept %s", in, bad)
			}
		}
	})
}

// undrawable names anything in a frame the terminal would act on: a control other than a line break or the
// ESC of a CSI, raw or UTF-8.
func undrawable(out string) string {
	if !utf8.ValidString(out) {
		return "invalid UTF-8"
	}
	for i, r := range out {
		switch {
		case r == 0x1b:
			if !strings.HasPrefix(out[i:], "\x1b[") {
				return "an escape"
			}
		case r < 0x20 && r != '\n', r >= 0x7f && r <= 0x9f:
			return fmt.Sprintf("%U", r)
		}
	}
	return ""
}

func TestDrawableDropsEveryControl(t *testing.T) {
	for in, want := range map[string]string{
		"a\u009b2Jb":              "a2Jb",
		"a\u0085b":                "ab",
		"a\x90+q544e\x1b\\b":      "a\uFFFD+q544eb",
		"a\r\x1b[2Kfake":          "afake",
		"a\x1bcb\x7f":             "ab",
		"\x1b[31mred\x1b[m\nnext": "\x1b[31mred\x1b[m\nnext",
	} {
		if got := drawable(in); got != want {
			t.Errorf("drawable(%q) = %q, want %q", in, got, want)
		}
	}
}

// rendered is the renderer's screen for the app's current frame, laid out by the given width method.
func rendered(m *Model, method ansi.Method) uv.ScreenBuffer {
	sb := uv.NewScreenBuffer(m.w, m.h)
	sb.Method = method
	uv.NewStyledString(m.View().Content).Draw(sb, sb.Bounds())
	return sb
}

func TestEveryFrameFillsTheTerminal(t *testing.T) {
	h := newHarness(t)
	forged := "Eve\n10:00:00  SIGNED ✓ Alice verified"
	cs := append(h.m.chat.Contacts(), radio.Contact{Key: [32]byte{0xEF}, Name: forged, Type: radio.TypeChat, PathLen: -1})
	h.m.chat.ReplaceContacts(cs)
	h.openConvo("Public")
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 1, Text: "Zed\n10:00 Bob: x"})
	h.keys("2")
	h.m.contacts.filter.text = "eve"
	h.incoming(radio.Incoming{From: h.contact("Alice").Prefix(), Hops: 1, Text: "line one\nline two\nline three"})
	h.m.Update(notifyFlushMsg{})
	h.m.Update(eventMsg{radio.Log{At: h.clk.t, Text: "HEARD  " + forged}})
	h.advance(toastSlide)
	if !h.m.toasting() {
		t.Fatal("the message shows as a toast")
	}
	for _, size := range [][2]int{{140, 44}, {80, 24}} {
		h.m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, tab := range "213456" {
			h.keys(string(tab))
			if lines := strings.Split(h.m.View().Content, "\n"); len(lines) != size[1] {
				t.Errorf("tab %c at %dx%d draws %d rows:\n%s", tab, size[0], size[1], len(lines), plain(lines))
			}
		}
	}
}

func TestBluetoothNamesCantDriveTheTerminal(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now,
		Scan: func(ctx context.Context, found func(Target)) error {
			found(Target{Addr: "AA:BB:CC:DD:EE:01", Detail: "MeshCore\u009b2J\x90+q544e\x1b\\\r\nSIM  demo  fake row · -70 dBm"})
			return nil
		}})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	for cmd := m.startScan(); cmd != nil; {
		cmd = m.connect.scanned(cmd().(scanMsg))
	}
	scr := plain(screen(t, m))
	if !strings.Contains(scr, "EE:01      MeshCore2J�+q544e\\ SIM  demo  fake row") {
		t.Errorf("the name stays on its own row:\n%s", scr)
	}
	var out bytes.Buffer
	r := uv.NewTerminalRenderer(&out, nil)
	r.Render(rendered(m, ansi.WcWidth).RenderBuffer)
	r.Flush()
	if bad := undrawable(out.String()); bad != "" {
		t.Errorf("a Bluetooth name reaches the terminal with %s", bad)
	}
}

// A family emoji is one character to some terminals and four to the rest; rows end at the edge either way.
func TestRowsEndAtTheEdgeWithJoinedEmoji(t *testing.T) {
	defer setRenderWidth(renderWidth)
	family := "👨\u200d👩\u200d👧\u200d👦"
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		h := newHarness(t)
		if method == ansi.GraphemeWidth {
			h.m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset})
		}
		cs := append(h.m.chat.Contacts(), radio.Contact{Key: [32]byte{0xE1}, Name: family + " Family", Type: radio.TypeChat, PathLen: -1})
		h.m.chat.ReplaceContacts(cs)
		h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 1, Text: family + " Fam: hello " + family})
		h.incoming(radio.Incoming{From: cs[len(cs)-1].Prefix(), Hops: 1, Text: "hi " + family})
		for _, tab := range "12" {
			h.keys(string(tab))
			h.advance(time.Second)
			lines := strings.Split(h.m.View().Content, "\n")
			sb := rendered(h.m, method)
			seen := false
			for y, l := range lines {
				if !strings.Contains(l, "👨") {
					continue
				}
				seen = true
				want := ansi.Strip(l)
				last, _ := utf8.DecodeLastRuneInString(want)
				if got := sb.CellAt(h.m.w-1, y); got == nil || got.Content != string(last) {
					t.Errorf("method %v, tab %c: row %d ends with %+v, not %q:\n%s", method, tab, y, got, last, sb.Line(y))
				}
			}
			if !seen {
				t.Errorf("method %v, tab %c: no row shows the name", method, tab)
			}
		}
	}
}

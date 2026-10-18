package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/verdantran/meshcore-tui/internal/radio"
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
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
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
	f.Fuzz(func(t *testing.T, s string) {
		out := drawable(s + "\x1b]")
		for _, seq := range stringSeqs {
			if strings.Contains(out, seq) {
				t.Fatalf("%q kept %q", s, seq)
			}
		}
	})
}

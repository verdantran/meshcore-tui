package ui

import (
	"strings"
	"testing"

	"github.com/verdantran/meshcore-tui/internal/chess"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// fromAlice is a direct message from Alice, as the radio hands it over.
func (h *harness) fromAlice(text string) {
	h.t.Helper()
	alice := h.contact("Alice")
	h.incoming(radio.Incoming{From: [6]byte(alice.Key[:6]), Hops: -1, Text: text})
}

func TestChessMessagesReadAsWhatTheyDo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fromAlice("♟abc new white")
	if !strings.Contains(plainLog(h), "Alice: challenged you to chess (you're black)") {
		t.Errorf("the log says what it is:\n%s", plainLog(h))
	}
	start := chess.Start()
	e4, _ := start.Find("e4")
	after, _ := start.Play(e4)
	move := chess.Msg{Game: "abc", Kind: chess.KindMove, Ply: 0, SAN: "e4", Check: chess.Checksum(after.FEN())}.Text()
	h.fromAlice(move)
	h.fromAlice("♟abc draw?")

	h.openConvo("Alice")
	screen := h.screen()
	for _, want := range []string{"♟ Alice challenged you, you're black", "♟ 1. e4", "♟ Alice offers a draw"} {
		if !strings.Contains(screen, want) {
			t.Errorf("missing %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, move) {
		t.Errorf("the raw move with its checksum is hidden:\n%s", screen)
	}
	if !strings.Contains(plainLog(h), "Alice: played 1. e4") {
		t.Errorf("the log names the move:\n%s", plainLog(h))
	}
}

func TestChessTextInAChannelIsJustText(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Text: "Bob: ♟abc resign"})
	h.openConvo("Public")
	if !strings.Contains(h.screen(), "♟abc resign") {
		t.Errorf("a channel message is shown as sent:\n%s", h.screen())
	}
}

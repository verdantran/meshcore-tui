package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/chess"
	"github.com/verdantran/meshcore-tui/internal/golden"
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

// dmFrom is a direct message from a contact, which starts a conversation with them if there isn't one.
func (h *harness) dmFrom(name, text string) {
	h.t.Helper()
	ct := h.contact(name)
	h.incoming(radio.Incoming{From: [6]byte(ct.Key[:6]), Hops: 1, Text: text})
}

// chessWith opens chess in the conversation with a contact.
func (h *harness) chessWith(name string) *boardView {
	h.t.Helper()
	if name != "Alice" {
		h.dmFrom(name, "fancy a game?")
	}
	h.openConvo(name)
	h.keys("c")
	if h.m.chats.board == nil {
		h.t.Fatalf("c didn't open chess with %s:\n%s", name, h.screen())
	}
	return h.m.chats.board
}

func (h *harness) gamePly() int {
	if g := h.m.chats.board.game; g != nil {
		return g.Ply
	}
	return -1
}

// clickSquare clicks a square of the board where it was last drawn, as the mouse would.
func (h *harness) clickSquare(sq chess.Square) {
	h.t.Helper()
	h.screen()
	b := h.m.chats.board
	row, col := 7-sq.Rank, sq.File
	if b.viewer == chess.Black {
		row, col = sq.Rank, 7-sq.File
	}
	listW := min(max((h.m.w-4)/4, 20), 32)
	x := 2 + listW + 3 + b.at[1] + col*sqW + 1
	y := 2 + 1 + b.at[0] + row
	_, cmd := h.m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	h.start(cmd)
	h.settle()
}

func TestPlayingChessWithTheDemo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.chessWith("Alice")
	if s := h.screen(); !strings.Contains(s, "♟ CHESS · Alice") || !strings.Contains(s, "No game with Alice yet") {
		t.Fatalf("an empty board:\n%s", s)
	}
	h.keys("n")
	h.keys("w")
	h.until("the challenge", func() bool { g := h.m.chats.board.game; return g != nil && g.Mine == chess.White })
	if !strings.Contains(h.screen(), "Your move") {
		t.Fatalf("white moves first:\n%s", h.screen())
	}

	// By cursor: it starts on the king's pawn; pick it up, take it two squares up, put it down.
	h.key(tea.KeyEnter)
	h.keys("kk")
	h.key(tea.KeyEnter)
	h.until("Alice's reply", func() bool { return h.gamePly() == 2 })
	if p := h.m.chats.board.game.Played[0]; p.SAN != "e4" || !p.Mine {
		t.Fatalf("first move: %+v", p)
	}

	// Typed.
	h.keys("i")
	h.typeFast("Nf3")
	h.key(tea.KeyEnter)
	h.until("Alice's second reply", func() bool { return h.gamePly() == 4 })

	// By mouse: d2, then d4.
	h.clickSquare(chess.Square{File: 3, Rank: 1})
	h.clickSquare(chess.Square{File: 3, Rank: 3})
	h.until("Alice's third reply", func() bool { return h.gamePly() == 6 })
	if p := h.m.chats.board.game.Played[4]; p.SAN != "d4" {
		t.Fatalf("the clicked move: %+v", p)
	}
	screen := h.screen()
	for _, want := range []string{"1. e4", "2. Nf3", "3. d4", "✓ Alice's radio got 3. d4"} {
		if !strings.Contains(screen, want) {
			t.Errorf("missing %q:\n%s", want, screen)
		}
	}
	if !strings.Contains(screen, "♟ 3. d4") {
		t.Errorf("the conversation beside the board shows the moves:\n%s", screen)
	}

	// Flip, then board alone, and back.
	h.keys("f")
	if h.screen(); h.m.chats.board.viewer != chess.Black {
		t.Error("f turns the board round")
	}
	h.keys("f")
	h.keys("L")
	if h.m.chessLayout() != "board" || strings.Contains(h.screen(), "♟ 3. d4") {
		t.Errorf("L shows the board alone:\n%s", h.screen())
	}
	h.keys("L")

	h.keys("R")
	h.keys("y")
	h.until("resigned", func() bool { g := h.m.chats.board.game; return g.End != nil })
	if !strings.Contains(h.screen(), "You resigned · Alice wins") {
		t.Errorf("after resigning:\n%s", h.screen())
	}
	h.key(tea.KeyEscape)
	if h.m.chats.board != nil {
		t.Error("esc goes back to the conversation")
	}
}

func TestChessIsForPeople(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openConvo("Public")
	h.keys("c")
	if h.m.chats.board != nil || !strings.Contains(plainLog(h), "only for direct messages with a person") {
		t.Errorf("chess in a channel: %v\n%s", h.m.chats.board != nil, plainLog(h))
	}
}

func TestATypedMoveThatIsntLegalSaysSo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.chessWith("Bob")
	h.keys("n")
	h.keys("w")
	h.until("the challenge", func() bool { g := h.m.chats.board.game; return g != nil && g.Mine == chess.White })
	h.keys("i")
	h.typeFast("Ke2")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.screen(), "Ke2 isn't a legal move here") || h.gamePly() != 0 {
		t.Errorf("an illegal move is refused:\n%s", h.screen())
	}
}

// TestGoldenChessBoard keeps the board's layout: beside the chat, alone, from black's side and in ASCII. The
// game is made from messages put in the conversation directly, as the demo's replies carry the real time.
func TestGoldenChessBoard(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.dmFrom("Bob", "fancy a game?")
	h.openConvo("Bob")
	bob := h.contact("Bob")
	cur, _ := h.m.selectedConvo()
	pos := chess.Start()
	say := func(mine bool, msg chess.Msg) {
		if mine {
			if _, _, err := h.m.chat.Compose(cur.Convo, msg.Text(), h.clk.t); err != nil {
				t.Fatal(err)
			}
			h.m.refreshChats()
			return
		}
		h.incoming(radio.Incoming{From: [6]byte(bob.Key[:6]), Hops: 1, Text: msg.Text()})
	}
	play := func(mine bool, san string, ply int) {
		mv, err := pos.Find(san)
		if err != nil {
			t.Fatal(err)
		}
		g := &chess.Game{ID: "g0d", Pos: pos, Ply: ply}
		msg, _ := g.Next(mv)
		pos, _ = pos.Play(mv)
		say(mine, msg)
	}
	say(true, chess.Msg{Game: "g0d", Kind: chess.KindNew, Colour: chess.White})
	for i, san := range []string{"e4", "e5", "Nf3", "Nc6", "Bb5", "a6"} {
		play(i%2 == 0, san, i)
	}
	say(false, chess.Msg{Game: "g0d", Kind: chess.KindDrawOffer})
	h.keys("c")
	h.keys("lk")                // from e2 to the knight on f3
	h.key(tea.KeyEnter)         // and pick it up: its moves show
	h.advance(10 * time.Second) // past the messages' arrival
	var frames []golden.Frame
	frames = append(frames, golden.Frame{Label: "board and chat", Lines: screen(t, h.m)})
	h.keys("L")
	frames = append(frames, golden.Frame{Label: "board alone", Lines: screen(t, h.m)})
	h.key(tea.KeyEscape)
	h.keys("f")
	frames = append(frames, golden.Frame{Label: "from black's side", Lines: screen(t, h.m)})
	h.m.opt.ASCII = true
	frames = append(frames, golden.Frame{Label: "ascii", Lines: screen(t, h.m)})
	golden.Check(t, "chess-board", frames)
}

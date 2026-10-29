package chess

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseEachKind(t *testing.T) {
	for _, c := range []struct {
		text string
		want Msg
	}{
		{"♟g3k new white", Msg{Game: "g3k", Kind: KindNew, Colour: White}},
		{"♟g3k new black", Msg{Game: "g3k", Kind: KindNew, Colour: Black}},
		{"♟g3k 1. e4 #1a2b", Msg{Game: "g3k", Kind: KindMove, Ply: 0, SAN: "e4", Check: "1a2b"}},
		{"♟g3k 12... Nf6 #a41c", Msg{Game: "g3k", Kind: KindMove, Ply: 23, SAN: "Nf6", Check: "a41c"}},
		{"♟g3k 40. exd8=Q+ #0000", Msg{Game: "g3k", Kind: KindMove, Ply: 78, SAN: "exd8=Q+", Check: "0000"}},
		{"♟g3k resign", Msg{Game: "g3k", Kind: KindResign}},
		{"♟g3k draw?", Msg{Game: "g3k", Kind: KindDrawOffer}},
		{"♟g3k draw ok", Msg{Game: "g3k", Kind: KindDrawAccept}},
		{"♟g3k sync rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1",
			Msg{Game: "g3k", Kind: KindSync, FEN: "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1"}},
		{"♟︎g3k resign", Msg{Game: "g3k", Kind: KindResign}}, // text-style pawn, as iOS is sent
		{"  ♟g3k resign \n", Msg{Game: "g3k", Kind: KindResign}},
	} {
		got, ok := Parse(c.text)
		if !ok || got != c.want {
			t.Errorf("Parse(%q) = %+v %v, want %+v", c.text, got, ok, c.want)
		}
	}
}

func TestParseRefusesWhatIsntChess(t *testing.T) {
	for _, s := range []string{
		"", "hello", "♟", "♟g3k", "♟G3K resign", "♟g3 resign", "♟g3kk resign", "♟g3k resign please",
		"♟g3k new red", "♟g3k 0. e4 #1a2b", "♟g3k 1. e4", "♟g3k 1. e4 #1A2B", "♟g3k 1. e4 #1a2", "♟g3k 1000. e4 #1a2b",
		"♟g3k 1. e4e4e4e4e #1a2b", "♟g3k sync not a fen", "♟g3k sync 8/8/8/8/8/8/8/8 x - - 0 1", "say ♟g3k resign",
	} {
		if m, ok := Parse(s); ok {
			t.Errorf("Parse(%q) = %+v, want no message", s, m)
		}
	}
}

func TestTextRoundTrips(t *testing.T) {
	for _, m := range []Msg{
		{Game: "abc", Kind: KindNew, Colour: Black},
		{Game: "abc", Kind: KindMove, Ply: 0, SAN: "Nf3", Check: "a41c"},
		{Game: "abc", Kind: KindMove, Ply: 23, SAN: "O-O-O+", Check: "0b3e"},
		{Game: "abc", Kind: KindResign}, {Game: "abc", Kind: KindDrawOffer}, {Game: "abc", Kind: KindDrawAccept},
		{Game: "abc", Kind: KindSync, FEN: Start().FEN()},
	} {
		got, ok := Parse(m.Text())
		if !ok || got != m {
			t.Errorf("%q came back as %+v %v", m.Text(), got, ok)
		}
	}
	if got := (Msg{Game: "g3k", Kind: KindMove, Ply: 23, SAN: "Nf6", Check: "a41c"}).Text(); got != "♟g3k 12... Nf6 #a41c" {
		t.Errorf("a black move is %q", got)
	}
}

func TestLabel(t *testing.T) {
	for ply, want := range map[int]string{0: "1. e4", 1: "1... e4", 22: "12. e4", 23: "12... e4"} {
		if got := Label(ply, "e4"); got != want {
			t.Errorf("Label(%d) = %q, want %q", ply, got, want)
		}
	}
}

// jsChecksum is meshcore-companion's fenCheck as written in its JavaScript: FNV-1a over each UTF-16 code
// unit with Math.imul, then the first four of eight hex digits.
func jsChecksum(fen string) string {
	h := uint32(2166136261)
	for _, ch := range strings.Join(strings.Split(fen, " ")[:3], " ") {
		h = (h ^ uint32(ch)) * 16777619
	}
	return fmt.Sprintf("%08x", h)[:4]
}

func TestChecksumMatchesTheJavaScript(t *testing.T) {
	for _, fen := range []string{
		Start().FEN(),
		"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1",
		"r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1",
		"8/8/8/8/8/8/8/K6k w - - 0 1",
	} {
		if got, want := Checksum(fen), jsChecksum(fen); got != want {
			t.Errorf("Checksum(%q) = %s, want %s", fen, got, want)
		}
	}
	a := Checksum("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1")
	if b := Checksum("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 7 9"); a != b {
		t.Error("en passant and the move counters don't change the checksum")
	}
}

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id := NewID()
		if _, ok := Parse(Prefix + id + " resign"); !ok {
			t.Fatalf("id %q isn't one a message can carry", id)
		}
		seen[id] = true
	}
	if len(seen) < 150 {
		t.Errorf("only %d different ids in 200", len(seen))
	}
}

func TestPlyOf(t *testing.T) {
	for fen, want := range map[string]int{
		Start().FEN(): 0,
		"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1": 1,
		"8/8/8/8/8/8/8/K6k w - - 0 12":                                22,
		"8/8/8/8/8/8/8/K6k b - - 0 12":                                23,
		"garbage":                                                     0,
	} {
		if got := plyOf(fen); got != want {
			t.Errorf("plyOf(%q) = %d, want %d", fen, got, want)
		}
	}
}

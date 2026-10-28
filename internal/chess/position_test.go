package chess

import (
	"slices"
	"strings"
	"testing"
)

func mustFEN(t *testing.T, fen string) *Position {
	t.Helper()
	p, err := FromFEN(fen)
	if err != nil {
		t.Fatalf("FromFEN(%q): %v", fen, err)
	}
	return p
}

func perft(p *Position, depth int) int {
	if depth == 0 {
		return 1
	}
	n := 0
	for _, m := range p.Moves() {
		next, err := p.Play(m)
		if err != nil {
			panic(err)
		}
		n += perft(next, depth-1)
	}
	return n
}

// The published move counts for positions chosen to catch move generators out: castling through check,
// en passant that uncovers a check, promotions with capture.
func TestPerft(t *testing.T) {
	for _, c := range []struct {
		fen    string
		counts []int
	}{
		{Start().FEN(), []int{20, 400, 8902}},
		{"r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1", []int{48, 2039}},
		{"8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1", []int{14, 191, 2812}},
		{"r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1", []int{6, 264}},
	} {
		p := mustFEN(t, c.fen)
		for d, want := range c.counts {
			if got := perft(p, d+1); got != want {
				t.Errorf("%s depth %d: %d, want %d", c.fen, d+1, got, want)
			}
		}
	}
}

func sans(p *Position) []string {
	var out []string
	for _, m := range p.Moves() {
		out = append(out, m.SAN)
	}
	return out
}

func TestSAN(t *testing.T) {
	for _, c := range []struct {
		fen  string
		want []string
	}{
		// Knights on b1 and f1 can both reach d2: the file tells them apart.
		{"4k3/8/8/8/8/8/8/1N2KN2 w - - 0 1", []string{"Nbd2", "Nfd2"}},
		// Rooks on a1 and a5 share a file: the rank tells them apart.
		{"4k3/8/8/R7/8/8/8/R3K3 w - - 0 1", []string{"R1a3", "R5a3"}},
		{"4k3/8/8/8/8/8/8/R3K2R w KQ - 0 1", []string{"O-O", "O-O-O"}},
		{"3qk3/4P3/8/8/8/8/8/4K3 w - - 0 1", []string{"exd8=Q+", "exd8=N"}},
		{"6k1/5ppp/8/8/8/8/8/R5K1 w - - 0 1", []string{"Ra8#"}},
		{"4k3/8/8/3pP3/8/8/8/4K3 w - d6 0 1", []string{"exd6"}},
	} {
		got := sans(mustFEN(t, c.fen))
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: no %s among %v", c.fen, w, got)
			}
		}
	}
}

func TestFind(t *testing.T) {
	p := Start()
	for _, in := range []string{"Nf3", "Nf3+", "g1f3", " Nf3 "} {
		if m, err := p.Find(in); err != nil || m.SAN != "Nf3" {
			t.Errorf("Find(%q) = %+v %v", in, m, err)
		}
	}
	for _, in := range []string{"", "Nf4", "e5", "nf3", "z9z9", "e2e5"} {
		if _, err := p.Find(in); err == nil {
			t.Errorf("Find(%q) should be refused", in)
		}
	}
	castle := mustFEN(t, "4k3/8/8/8/8/8/8/R3K2R w KQ - 0 1")
	for in, want := range map[string]string{"O-O": "O-O", "0-0": "O-O", "0-0-0": "O-O-O", "e1g1": "O-O"} {
		if m, err := castle.Find(in); err != nil || m.SAN != want {
			t.Errorf("Find(%q) = %+v %v", in, m, err)
		}
	}
	promo := mustFEN(t, "4k3/P7/8/8/8/8/8/4K3 w - - 0 1")
	if m, err := promo.Find("a7a8n"); err != nil || m.Promo != 'N' {
		t.Errorf("a7a8n = %+v %v", m, err)
	}
	if _, err := promo.Find("a7a8"); err == nil {
		t.Error("a promotion has to say what to")
	}
}

func TestFromFENRefusesImpossiblePositions(t *testing.T) {
	for why, fen := range map[string]string{
		"not FEN":           "hello",
		"no black king":     "8/8/8/8/8/8/8/4K3 w - - 0 1",
		"two white kings":   "4k3/8/8/8/8/8/8/K3K3 w - - 0 1",
		"pawn on rank 8":    "P3k3/8/8/8/8/8/8/4K3 w - - 0 1",
		"pawn on rank 1":    "4k3/8/8/8/8/8/8/p3K3 w - - 0 1",
		"mover left check":  "4k3/8/8/8/8/8/8/4R1K1 w - - 0 1",
		"castle, no rook":   "4k3/8/8/8/8/8/8/4K3 w K - 0 1",
		"castle, king away": "4k3/8/8/8/8/8/8/3K3R w K - 0 1",
		"ep with no pawn":   "4k3/8/8/8/8/8/8/4K3 w - d6 0 1",
		"ep on wrong rank":  "4k3/8/8/3pP3/8/8/8/4K3 w - d5 0 1",
		"nine ranks":        "4k3/8/8/8/8/8/8/8/4K3 w - - 0 1",
		"rank too long":     "4k4/8/8/8/8/8/8/4K3 w - - 0 1",
		"move zero":         "4k3/8/8/8/8/8/8/4K3 w - - 0 0",
	} {
		if _, err := FromFEN(fen); err == nil {
			t.Errorf("%s: %q was accepted", why, fen)
		}
	}
	for _, fen := range []string{
		Start().FEN(),
		"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1",
		"r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1",
		"4k3/8/8/8/8/8/8/4R1K1 b - - 0 1", // black is in check and to move, which is fine
	} {
		mustFEN(t, fen)
	}
}

func TestInCheck(t *testing.T) {
	if Start().InCheck() {
		t.Error("nobody's in check at the start")
	}
	if !mustFEN(t, "4k3/8/8/8/8/8/8/4R1K1 b - - 0 1").InCheck() {
		t.Error("black is in check from the rook")
	}
	if mustFEN(t, "4k3/8/8/8/8/8/8/3R2K1 b - - 0 1").InCheck() {
		t.Error("the rook isn't on the king's file")
	}
}

func TestOutcome(t *testing.T) {
	for want, fen := range map[Outcome]string{
		Checkmate:            "rnb1kbnr/pppp1ppp/8/4p3/6Pq/5P2/PPPPP2P/RNBQKBNR w KQkq - 1 3", // fool's mate
		Stalemate:            "7k/5Q2/6K1/8/8/8/8/8 b - - 0 1",
		InsufficientMaterial: "4k3/8/8/8/8/8/8/4KN2 w - - 0 1",
		FiftyMoves:           "4k3/8/8/8/8/8/8/R3K3 w - - 100 80",
		NoOutcome:            Start().FEN(),
	} {
		if got := mustFEN(t, fen).Outcome(); got != want {
			t.Errorf("%s: %v, want %v", fen, got, want)
		}
	}
	for fen, want := range map[string]bool{
		"4k3/8/8/8/8/8/8/4K3 w - - 0 1":     true,  // kings alone
		"4k3/8/8/8/8/8/8/2B1K3 w - - 0 1":   true,  // a lone bishop
		"2b1k3/8/8/8/8/8/8/2B1K3 w - - 0 1": false, // bishops on different colours could mate
		"3bk3/8/8/8/8/8/8/2B1K3 w - - 0 1":  true,  // bishops all on dark squares can't
		"4k3/8/8/8/8/8/8/1NN1K3 w - - 0 1":  false, // two knights is chess.js's call: not insufficient
		"4k3/8/8/8/8/8/4P3/4K3 w - - 0 1":   false,
	} {
		if got := mustFEN(t, fen).insufficient(); got != want {
			t.Errorf("%s insufficient = %v, want %v", fen, got, want)
		}
	}
}

// InCheck works the board out itself; across every position in two perft trees it must agree with the rules,
// which mark a move that gives check with + or #.
func TestInCheckAgreesWithTheRules(t *testing.T) {
	var walk func(p *Position, depth int) int
	walk = func(p *Position, depth int) int {
		n := 0
		for _, m := range p.Moves() {
			next, err := p.Play(m)
			if err != nil {
				t.Fatal(err)
			}
			if gives := strings.HasSuffix(m.SAN, "+") || strings.HasSuffix(m.SAN, "#"); next.InCheck() != gives {
				t.Fatalf("%s then %s: in check %v", p.FEN(), m.SAN, next.InCheck())
			}
			n++
			if depth > 1 {
				n += walk(next, depth-1)
			}
		}
		return n
	}
	for _, fen := range []string{
		"r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1",
		"8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1",
	} {
		if n := walk(mustFEN(t, fen), 3); n < 1000 {
			t.Errorf("only %d positions checked", n)
		}
	}
}

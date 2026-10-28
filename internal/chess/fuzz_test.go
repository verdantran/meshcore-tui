package chess

import (
	"strings"
	"testing"
	"time"
)

// Everything here reads what anyone on the mesh can send: a chess message, the position in a sync, a history
// of either. None of it may panic, whatever it's given.

func FuzzParse(f *testing.F) {
	for _, s := range []string{"♟g3k new white", "♟g3k 12... Nf6 #a41c", "♟g3k draw ok", "♟g3k sync " + Start().FEN()} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, ok := Parse(s)
		if !ok {
			return
		}
		if again, ok := Parse(m.Text()); !ok || again != m {
			t.Errorf("%q parsed as %+v, which writes as %q", s, m, m.Text())
		}
	})
}

func FuzzFromFEN(f *testing.F) {
	for _, s := range []string{
		Start().FEN(),
		"r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1",
		"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1",
		"4k3/8/8/8/8/8/8/K3K3 w - - 0 1",
		"8/8/8/8/8/8/8/8 w - - 0 1",
		"kkkkkkkk/8/8/8/8/8/8/KKKKKKKK w KQkq a3 9999 1",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, fen string) {
		p, err := FromFEN(fen)
		if err != nil {
			return
		}
		// A position that passed the checks must survive everything the board does with it.
		p.InCheck()
		p.Outcome()
		for _, m := range p.Moves() {
			next, err := p.Play(m)
			if err != nil {
				t.Fatalf("%s: %s is listed but can't be played: %v", fen, m.SAN, err)
			}
			next.Outcome()
			Checksum(next.FEN())
		}
	})
}

func FuzzGames(f *testing.F) {
	f.Add("♟abc new white\n♟abc 1. e4 #0000\n♟abc sync "+Start().FEN()+"\n♟abc draw?", uint8(5))
	f.Fuzz(func(t *testing.T, history string, mine uint8) {
		var entries []Entry
		for i, line := range strings.Split(history, "\n") {
			entries = append(entries, Entry{Text: line, Mine: mine>>(i%8)&1 == 1, At: t0.Add(time.Duration(i%5) * time.Second),
				Failed: i%3 == 0})
		}
		for _, g := range Games(entries) {
			g.MyTurn()
			g.Last()
			for _, m := range g.Pos.Moves() {
				g.Next(m)
				break
			}
		}
	})
}

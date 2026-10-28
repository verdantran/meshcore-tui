package chess

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// log is a conversation as one side sees it; each line is a second after the last.
type log struct{ entries []Entry }

func (l *log) add(mine bool, text string) *Entry {
	l.entries = append(l.entries, Entry{Text: text, Mine: mine, At: t0.Add(time.Duration(len(l.entries)) * time.Second),
		Ref: int64(len(l.entries) + 1)})
	return &l.entries[len(l.entries)-1]
}

// play has whoever is to move in the latest game play a move, as the board would send it.
func (l *log) play(t *testing.T, mine bool, move string) *Entry {
	t.Helper()
	g := Latest(l.entries)
	m, err := g.Pos.Find(move)
	if err != nil {
		t.Fatalf("%s: %v", move, err)
	}
	msg, err := g.Next(m)
	if err != nil {
		t.Fatal(err)
	}
	return l.add(mine, msg.Text())
}

func TestAGameFromItsMessages(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	if g := Latest(l.entries); g.Mine != White || !g.MyTurn() || g.Ply != 0 {
		t.Fatalf("after the challenge: %+v", g)
	}
	l.play(t, true, "e4")
	l.play(t, false, "e5")
	l.play(t, true, "Nf3")
	g := Latest(l.entries)
	if g.Ply != 3 || len(g.Played) != 3 || g.MyTurn() || g.Pos.At(Square{5, 2}) != (Piece{'N', White}) {
		t.Fatalf("after three moves: ply %d, %d played, my turn %v", g.Ply, len(g.Played), g.MyTurn())
	}
	if last, _ := g.Last(); last.Label() != "2. Nf3" || last.From != (Square{6, 0}) || last.To != (Square{5, 2}) {
		t.Errorf("last move: %+v", last)
	}
	if l.entries[1].Text != "♟abc 1. e4 #"+Checksum("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1") {
		t.Errorf("first move sent as %q", l.entries[1].Text)
	}

	// The other side sees the same game from the other chair.
	var theirs []Entry
	for _, e := range l.entries {
		e.Mine = !e.Mine
		theirs = append(theirs, e)
	}
	if g := Latest(theirs); g.Mine != Black || !g.MyTurn() || g.Ply != 3 {
		t.Errorf("black's view: mine %v, my turn %v, ply %d", g.Mine, g.MyTurn(), g.Ply)
	}
}

func TestAResentMoveCountsOnce(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	first := l.play(t, true, "e4")
	first.Failed = true
	l.add(true, first.Text) // resent, and this one got through
	g := Latest(l.entries)
	if g.Ply != 1 || len(g.Played) != 1 {
		t.Fatalf("ply %d, %d played", g.Ply, len(g.Played))
	}
	if g.Played[0].Entry.Failed || g.Played[0].Entry.Ref != 3 {
		t.Errorf("the copy that got through is the one kept: %+v", g.Played[0].Entry)
	}
}

func TestAMismatchIsCaughtAndASyncRepairsIt(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	l.play(t, true, "e4")
	l.add(false, "♟abc 1... e5 #ffff") // the checksum of a board that isn't ours
	g := Latest(l.entries)
	if g.Problem == nil || !g.Problem.Mismatch || g.MyTurn() {
		t.Fatalf("problem: %+v", g.Problem)
	}
	// They send their position; it replaces everything before it.
	l.add(false, "♟abc sync rnbqkbnr/pppp1ppp/8/4p3/4P3/8/PPPP1PPP/RNBQKBNR w KQkq e6 0 2")
	g = Latest(l.entries)
	if g.Problem != nil || g.Ply != 2 || !g.MyTurn() || len(g.Played) != 0 {
		t.Fatalf("after the sync: problem %+v, ply %d, my turn %v", g.Problem, g.Ply, g.MyTurn())
	}
	l.play(t, true, "Nf3")
	if g := Latest(l.entries); g.Ply != 3 || g.Played[0].Label() != "2. Nf3" {
		t.Errorf("play goes on from the sync: ply %d", g.Ply)
	}
}

func TestABadSyncIsIgnored(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	l.add(false, "♟abc sync 4k3/8/8/8/8/8/8/K3K3 w - - 0 1") // two white kings
	if g := Latest(l.entries); g.Ply != 0 || g.Pos.FEN() != Start().FEN() {
		t.Errorf("a position no game could reach is ignored: %s", g.Pos.FEN())
	}
}

func TestAnIllegalMoveStopsTheGame(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	l.play(t, true, "e4")
	l.add(false, "♟abc 1... Ke7 #0000")
	l.add(false, "♟abc 1... Qh4 #0000")
	g := Latest(l.entries)
	if g.Problem == nil || !g.Problem.Illegal || g.Problem.Move.SAN != "Ke7" {
		t.Errorf("problem: %+v", g.Problem)
	}
}

func TestAMissingMoveIsAGap(t *testing.T) {
	var l log
	l.add(true, "♟abc new black")
	white := Start()
	e4, _ := white.Find("e4")
	afterE4, _ := white.Play(e4)
	e5, _ := afterE4.Find("e5")
	afterE5, _ := afterE4.Play(e5)
	nf3, _ := afterE5.Find("Nf3")
	afterNf3, _ := afterE5.Play(nf3)
	// White's first move went missing; their second arrived.
	l.add(false, Msg{Game: "abc", Kind: KindMove, Ply: 2, SAN: "Nf3", Check: Checksum(afterNf3.FEN())}.Text())
	g := Latest(l.entries)
	if g.Gap != 0 || g.MyTurn() || g.Problem != nil {
		t.Errorf("gap %d, my turn %v", g.Gap, g.MyTurn())
	}
}

func TestALostChallengeIsWorkedOutFromTheFirstMove(t *testing.T) {
	var l log
	start := Start()
	e4, _ := start.Find("e4")
	after, _ := start.Play(e4)
	l.add(false, Msg{Game: "abc", Kind: KindMove, Ply: 0, SAN: "e4", Check: Checksum(after.FEN())}.Text())
	if g := Latest(l.entries); g.Mine != NoColour {
		t.Fatalf("their move alone doesn't say which side we are: %v", g.Mine)
	}
	l.play(t, true, "e5")
	if g := Latest(l.entries); g.Mine != Black {
		t.Errorf("our first move was black's: %v", g.Mine)
	}
}

func TestDrawsAndResigning(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	l.play(t, true, "e4")
	l.add(false, "♟abc draw?")
	if g := Latest(l.entries); g.Offer == nil || g.Offer.Mine {
		t.Fatalf("their offer: %+v", g.Offer)
	}
	l.play(t, false, "e5") // a move after the offer turns it down
	if g := Latest(l.entries); g.Offer != nil {
		t.Error("a move after the offer turns it down")
	}
	l.add(true, "♟abc draw?")
	l.add(false, "♟abc draw ok")
	if g := Latest(l.entries); g.End == nil || g.End.Why != EndDrawAgreed {
		t.Errorf("end: %+v", g.End)
	}
	l.add(true, "♟abc resign") // too late: the game's already over
	if g := Latest(l.entries); g.End.Why != EndDrawAgreed {
		t.Errorf("the first ending stands: %+v", g.End)
	}

	var r log
	r.add(true, "♟xyz new black")
	r.add(true, "♟xyz resign")
	if g := Latest(r.entries); g.End == nil || g.End.Why != EndResign || !g.End.Mine {
		t.Errorf("our resignation: %+v", g.End)
	}
}

func TestCheckmateEndsTheGame(t *testing.T) {
	var l log
	l.add(false, "♟abc new white")
	for i, mv := range []string{"f3", "e5", "g4", "Qh4#"} {
		l.play(t, i%2 == 1, mv)
	}
	g := Latest(l.entries)
	if g.End == nil || g.End.Why != EndCheckmate || g.MyTurn() {
		t.Errorf("end: %+v", g.End)
	}
	if last, _ := g.Last(); last.SAN != "Qh4#" {
		t.Errorf("the mating move: %s", last.SAN)
	}
}

func TestRepetitionEndsTheGame(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	for i, mv := range []string{"Nf3", "Nf6", "Ng1", "Ng8", "Nf3", "Nf6", "Ng1", "Ng8"} {
		l.play(t, i%2 == 0, mv)
	}
	if g := Latest(l.entries); g.End == nil || g.End.Why != EndRepetition {
		t.Errorf("the start position three times: %+v", g.End)
	}
}

func TestTheLatestGameIsShown(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	l.add(true, "♟abc resign")
	l.add(false, "♟def new white") // a rematch, they're white
	gs := Games(l.entries)
	if len(gs) != 2 || gs[1].ID != "def" || Latest(l.entries).Mine != Black {
		t.Errorf("%d games, latest %+v", len(gs), Latest(l.entries))
	}
	if Latest(nil) != nil || Latest([]Entry{{Text: "hello"}}) != nil {
		t.Error("no chess messages, no game")
	}
}

func TestMessagesAreOrderedByTime(t *testing.T) {
	var l log
	l.add(true, "♟abc new white")
	l.play(t, true, "e4")
	l.play(t, false, "e5")
	// Delivered out of order: the history is sorted before it's replayed.
	l.entries[1], l.entries[2] = l.entries[2], l.entries[1]
	if g := Latest(l.entries); g.Ply != 2 {
		t.Errorf("ply %d", g.Ply)
	}
}

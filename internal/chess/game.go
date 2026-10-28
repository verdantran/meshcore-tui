package chess

import (
	"slices"
	"time"
)

// Entry is a message in a conversation, as Games needs it.
type Entry struct {
	Text   string
	Mine   bool      // we sent it
	At     time.Time // when it was sent or received, which orders a sync against the moves around it
	Failed bool      // ours, and it never got a delivery receipt
	Ref    int64     // the caller's id for the message, handed back on the moves made from it
}

// Played is a move in a game, with the message that carried it.
type Played struct {
	Move
	Ply   int
	Check string
	Mine  bool
	Entry Entry
}

// Label names the move as the move list does.
func (p Played) Label() string { return Label(p.Ply, p.SAN) }

// Why is how a game ended.
type Why int

const (
	EndResign Why = iota + 1
	EndDrawAgreed
	EndCheckmate
	EndStalemate
	EndMaterial
	EndRepetition
	EndFiftyMoves
)

// End is how and when a game ended.
type End struct {
	Why  Why
	Mine bool // EndResign: we resigned
	At   time.Time
}

// Problem is a move that couldn't be played: it's illegal here, or the position after it isn't the one its
// sender had, so the two boards have drifted apart.
type Problem struct {
	Move     Played
	Illegal  bool
	Mismatch bool
}

// Offer is a draw offered and not yet answered.
type Offer struct {
	Mine bool
	At   time.Time
}

// Game is a game rebuilt from its messages.
type Game struct {
	ID       string
	Mine     Colour // the side we play, or NoColour if the challenge that set it is missing
	Pos      *Position
	Ply      int      // moves played, counting from the start or a sync
	Played   []Played // in order, from the start or the sync
	Problem  *Problem
	Gap      int // the ply of a missing move when later ones have arrived, or -1
	End      *End
	Offer    *Offer
	First    time.Time
	syncedAt time.Time
	synced   bool
}

// Last is the most recent move played, if any.
func (g *Game) Last() (Played, bool) {
	if len(g.Played) == 0 {
		return Played{}, false
	}
	return g.Played[len(g.Played)-1], true
}

// MyTurn reports whether it's our move in a game that's still going.
func (g *Game) MyTurn() bool {
	return g.End == nil && g.Problem == nil && g.Gap < 0 && g.Mine != NoColour && g.Pos.Turn() == g.Mine
}

// Next is the message that plays a move in the game.
func (g *Game) Next(m Move) (Msg, error) {
	after, err := g.Pos.Play(m)
	if err != nil {
		return Msg{}, err
	}
	return Msg{Game: g.ID, Kind: KindMove, Ply: g.Ply, SAN: m.SAN, Check: Checksum(after.FEN())}, nil
}

type candidate struct {
	msg   Msg
	entry Entry
}

// Games rebuilds every game in a conversation from its messages, oldest first, as meshcore-companion does:
// a move counts once per ply, so a resend or a second copy is the same move, and for our own moves a copy that
// got through wins over one that failed. A sync replaces everything sent before it, moves included.
func Games(entries []Entry) []*Game {
	entries = slices.Clone(entries)
	slices.SortStableFunc(entries, func(a, b Entry) int { return a.At.Compare(b.At) })
	byID := map[string]*Game{}
	var order []*Game
	cands := map[*Game][]candidate{}
	var syncs = map[*Game]Msg{}
	for _, e := range entries {
		msg, ok := Parse(e.Text)
		if !ok {
			continue
		}
		g := byID[msg.Game]
		if g == nil {
			g = &Game{ID: msg.Game, First: e.At, Gap: -1}
			byID[msg.Game] = g
			order = append(order, g)
		}
		switch msg.Kind {
		case KindNew:
			g.Mine = msg.Colour
			if !e.Mine {
				g.Mine = msg.Colour.Other()
			}
		case KindMove:
			cands[g] = append(cands[g], candidate{msg, e})
		case KindSync:
			syncs[g], g.syncedAt, g.synced = msg, e.At, true
		case KindResign:
			if g.End == nil {
				g.End = &End{Why: EndResign, Mine: e.Mine, At: e.At}
			}
		case KindDrawAccept:
			if g.End == nil {
				g.End = &End{Why: EndDrawAgreed, At: e.At}
			}
		case KindDrawOffer:
			g.Offer = &Offer{Mine: e.Mine, At: e.At}
		}
	}
	for _, g := range order {
		g.replay(cands[g], syncs[g])
	}
	return order
}

// Latest is the newest game in a conversation, the one a board shows, or nil if there's none.
func Latest(entries []Entry) *Game {
	gs := Games(entries)
	if len(gs) == 0 {
		return nil
	}
	return gs[len(gs)-1]
}

func (g *Game) replay(cands []candidate, sync Msg) {
	byPly := map[int]candidate{}
	var plies []int // in the order each ply was first heard, which decides the colour a lost challenge implies
	for _, c := range cands {
		if g.synced && c.entry.At.Before(g.syncedAt) {
			continue
		}
		had, ok := byPly[c.msg.Ply]
		if !ok {
			plies = append(plies, c.msg.Ply)
		}
		if !ok || (had.entry.Failed && !c.entry.Failed) {
			byPly[c.msg.Ply] = c
		}
	}
	g.Pos, g.Ply = Start(), 0
	if g.synced {
		if p, err := FromFEN(sync.FEN); err == nil {
			g.Pos, g.Ply = p, plyOf(sync.FEN)
		}
	}
	seen := map[string]int{g.Pos.repetitionKey(): 1}
	repeated := false
	for {
		c, ok := byPly[g.Ply]
		if !ok {
			break
		}
		played := Played{Ply: g.Ply, Check: c.msg.Check, Mine: c.entry.Mine, Entry: c.entry}
		m, err := g.Pos.Find(c.msg.SAN)
		if err != nil {
			played.Move.SAN = c.msg.SAN
			g.Problem = &Problem{Move: played, Illegal: true}
			break
		}
		next, err := g.Pos.Play(m)
		if err != nil {
			played.Move.SAN = c.msg.SAN
			g.Problem = &Problem{Move: played, Illegal: true}
			break
		}
		played.Move = m
		g.Pos = next
		g.Played = append(g.Played, played)
		if Checksum(next.FEN()) != c.msg.Check {
			g.Problem = &Problem{Move: played, Mismatch: true}
			break
		}
		g.Ply++
		seen[next.repetitionKey()]++
		if seen[next.repetitionKey()] >= 3 {
			repeated = true
		}
	}
	if g.Problem == nil {
		for ply := range byPly {
			if ply > g.Ply {
				g.Gap = g.Ply
				break
			}
		}
	}
	// We learn our colour from the challenge; if that got lost, from our first move.
	if g.Mine == NoColour {
		for _, ply := range plies {
			if byPly[ply].entry.Mine {
				g.Mine = White
				if ply%2 == 1 {
					g.Mine = Black
				}
				break
			}
		}
	}
	if last, ok := g.Last(); ok && g.Offer != nil && last.Entry.At.After(g.Offer.At) {
		g.Offer = nil // a move after the offer turns it down
	}
	if g.End == nil {
		// In chess.js's order, which meshcore-companion names the end by.
		switch out := g.Pos.Outcome(); {
		case out == Checkmate:
			g.End = &End{Why: EndCheckmate}
		case out == Stalemate:
			g.End = &End{Why: EndStalemate}
		case out == InsufficientMaterial:
			g.End = &End{Why: EndMaterial}
		case repeated:
			g.End = &End{Why: EndRepetition}
		case out == FiftyMoves:
			g.End = &End{Why: EndFiftyMoves}
		}
	}
}

package sim

import (
	"strings"
	"time"

	"github.com/verdantran/meshcore-tui/internal/chess"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// The people in the demo play chess back. Each keeps the game as you'd see it from their side, rebuilt from
// the messages both of you sent, and answers a move after a moment's thought: mate if it can, the biggest
// capture going, a check, or any legal move.

// chessHeard is a person reading a chess message you sent them, then answering it.
func (s *Sim) chessHeard(name string, text string) {
	if _, ok := chess.Parse(text); !ok {
		return
	}
	s.mu.Lock()
	s.chessLog[name] = append(s.chessLog[name], chess.Entry{Text: text, At: time.Now()})
	think := time.Duration(900+s.rng.Intn(2600)) * time.Millisecond
	s.mu.Unlock()
	go func() {
		time.Sleep(time.Duration(float64(think) / s.speed))
		s.chessAnswer(name)
	}()
}

// chessAnswer sends whatever the person says to the game as it stands, if anything.
func (s *Sim) chessAnswer(name string) {
	s.mu.Lock()
	g := chess.Latest(s.chessLog[name])
	var reply string
	switch {
	case g == nil || g.End != nil || g.Mine == chess.NoColour:
	case g.Problem != nil:
		reply = chess.Msg{Game: g.ID, Kind: chess.KindSync, FEN: g.Pos.FEN()}.Text() // theirs is the board they trust
	case g.Offer != nil && !g.Offer.Mine && g.Ply > 30 && s.rng.Float64() < 0.6:
		reply = chess.Msg{Game: g.ID, Kind: chess.KindDrawAccept}.Text()
	case g.Pos.Turn() != g.Mine:
	case g.Ply > 20 && material(g.Pos, g.Mine) <= material(g.Pos, g.Mine.Other())-9 && s.rng.Float64() < 0.5:
		reply = chess.Msg{Game: g.ID, Kind: chess.KindResign}.Text()
	default:
		if mv, ok := s.pickMove(g.Pos); ok {
			if msg, err := g.Next(mv); err == nil {
				reply = msg.Text()
			}
		}
	}
	if reply != "" {
		s.chessLog[name] = append(s.chessLog[name], chess.Entry{Text: reply, Mine: true, At: time.Now()})
	}
	s.mu.Unlock()
	if reply != "" {
		s.dmFrom(name, reply)
	}
}

// dmFrom is a direct message from a person in the demo arriving.
func (s *Sim) dmFrom(name, text string) {
	now := time.Now()
	s.emit(radio.Incoming{At: now, From: prefix(key(peopleHash(name), name)), Hops: s.intn(3) + 1, SentAt: now,
		Text: text, SNR: float32(s.intn(48)-12) / 4, HasSNR: true})
}

var pieceValue = map[byte]int{'Q': 9, 'R': 5, 'B': 3, 'N': 3, 'P': 1}

func material(p *chess.Position, c chess.Colour) int {
	n := 0
	for f := range 8 {
		for r := range 8 {
			if pc := p.At(chess.Square{File: f, Rank: r}); pc.Colour == c {
				n += pieceValue[pc.Kind]
			}
		}
	}
	return n
}

// pickMove chooses a move the way a casual player might. The caller holds s.mu, for the random numbers.
func (s *Sim) pickMove(p *chess.Position) (chess.Move, bool) {
	moves := p.Moves()
	if len(moves) == 0 {
		return chess.Move{}, false
	}
	best, bestScore := moves[s.rng.Intn(len(moves))], 0
	for _, mv := range moves {
		score := 0
		switch {
		case strings.HasSuffix(mv.SAN, "#"):
			score = 100
		case strings.Contains(mv.SAN, "x"):
			score = 10 + pieceValue[p.At(mv.To).Kind]
		case strings.HasSuffix(mv.SAN, "+"):
			score = 5
		}
		if mv.Promo == 'Q' {
			score += 8
		}
		if score > bestScore || score == bestScore && score > 0 && s.rng.Intn(2) == 0 {
			best, bestScore = mv, score
		}
	}
	return best, true
}

// openingGame is the demo starting with a game under way: Dan has challenged you and made his first move.
func (s *Sim) openingGame(now time.Time) {
	const name, id = "Dan", "d4n"
	start := chess.Start()
	e4, _ := start.Find("e4")
	first, err := (&chess.Game{ID: id, Pos: start}).Next(e4)
	if err != nil {
		return
	}
	msgs := []string{chess.Msg{Game: id, Kind: chess.KindNew, Colour: chess.White}.Text(), first.Text()}
	s.mu.Lock()
	for i, text := range msgs {
		s.chessLog[name] = append(s.chessLog[name], chess.Entry{Text: text, Mine: true, At: now.Add(time.Duration(i-len(msgs)) * time.Minute)})
	}
	s.mu.Unlock()
	for i, text := range msgs {
		at := now.Add(time.Duration(i-len(msgs)) * time.Minute)
		s.emit(radio.Incoming{At: at, From: prefix(key(peopleHash(name), name)), Hops: 2, SentAt: at, Text: text, SNR: 4.5, HasSNR: true})
	}
}

package chess

import (
	"errors"
	"fmt"
	"strings"

	lib "github.com/corentings/chess/v2"
)

// The rules are github.com/corentings/chess/v2's. It panics if its board ever reaches a state it can't hold,
// and positions arrive from anyone on the mesh, so every position it's given is checked first (sane) and every
// call into it that handles one is guarded (guard).

// Square is a square on the board: file 0 to 7 is a to h, rank 0 to 7 is 1 to 8.
type Square struct{ File, Rank int }

func (s Square) String() string { return string(rune('a'+s.File)) + string(rune('1'+s.Rank)) }

func (s Square) valid() bool { return s.File >= 0 && s.File < 8 && s.Rank >= 0 && s.Rank < 8 }

func (s Square) lib() lib.Square { return lib.NewSquare(lib.File(s.File), lib.Rank(s.Rank)) }

func fromLib(sq lib.Square) Square { return Square{File: int(sq.File()), Rank: int(sq.Rank())} }

// Piece is a piece on a square: Kind is K, Q, R, B, N or P.
type Piece struct {
	Kind   byte
	Colour Colour
}

// Position is a position in a game.
type Position struct {
	pos *lib.Position
}

// errGuarded is a call into the rules that panicked: the position or move it was given couldn't be played.
var errGuarded = errors.New("chess: that position can't be played")

// guard runs f, turning a panic in the rules into an error.
func guard(f func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errGuarded
		}
	}()
	return f()
}

// Start is the starting position.
func Start() *Position { return &Position{lib.StartingPosition()} }

// FromFEN reads a position that arrived in a sync message, refusing one that couldn't come from a game.
func FromFEN(fen string) (*Position, error) {
	var p *Position
	err := guard(func() error {
		pos := &lib.Position{}
		if err := pos.UnmarshalText([]byte(fen)); err != nil {
			return err
		}
		p = &Position{pos}
		return p.sane()
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// sane refuses a position no game could reach, which the rules might not cope with: one king a side, no pawns
// on the first or last rank, the side that just moved not in check, castling only with the king and rook at home,
// and en passant only behind a pawn that has just moved two squares.
func (p *Position) sane() error {
	kings := map[Colour]int{}
	for sq, pc := range p.pieces() {
		if pc.Kind == 'K' {
			kings[pc.Colour]++
		}
		if pc.Kind == 'P' && (sq.Rank == 0 || sq.Rank == 7) {
			return errors.New("chess: a pawn on the first or last rank")
		}
	}
	if kings[White] != 1 || kings[Black] != 1 {
		return errors.New("chess: each side needs one king")
	}
	if p.attacksKing() {
		return errors.New("chess: the side that just moved is in check")
	}
	cr := p.pos.CastleRights()
	for _, c := range []struct {
		colour Colour
		side   lib.Side
		rook   Square
	}{{White, lib.KingSide, Square{7, 0}}, {White, lib.QueenSide, Square{0, 0}}, {Black, lib.KingSide, Square{7, 7}}, {Black, lib.QueenSide, Square{0, 7}}} {
		if !cr.CanCastle(c.colour.lib(), c.side) {
			continue
		}
		home := Square{4, c.rook.Rank}
		if p.At(home) != (Piece{'K', c.colour}) || p.At(c.rook) != (Piece{'R', c.colour}) {
			return errors.New("chess: castling rights without the king and rook at home")
		}
	}
	if ep := p.pos.EnPassantSquare(); ep != lib.NoSquare {
		sq := fromLib(ep)
		mover, pawnRank, behind := Black, 4, 5 // black just moved two squares, from rank 7 to 5
		if p.Turn() == Black {
			mover, pawnRank, behind = White, 3, 2
		}
		if sq.Rank != behind || p.At(Square{sq.File, pawnRank}) != (Piece{'P', mover}) || p.At(sq) != (Piece{}) {
			return errors.New("chess: an en passant square with no pawn that just moved past it")
		}
	}
	return nil
}

func (c Colour) lib() lib.Color {
	if c == Black {
		return lib.Black
	}
	return lib.White
}

func colourOf(c lib.Color) Colour {
	if c == lib.Black {
		return Black
	}
	return White
}

var kindOf = map[lib.PieceType]byte{lib.King: 'K', lib.Queen: 'Q', lib.Rook: 'R', lib.Bishop: 'B', lib.Knight: 'N', lib.Pawn: 'P'}

func (p *Position) pieces() map[Square]Piece {
	out := map[Square]Piece{}
	for sq, pc := range p.pos.Board().SquareMap() {
		out[fromLib(sq)] = Piece{kindOf[pc.Type()], colourOf(pc.Color())}
	}
	return out
}

// At is the piece on a square, or the zero Piece if it's empty.
func (p *Position) At(sq Square) Piece {
	if !sq.valid() {
		return Piece{}
	}
	pc := p.pos.Board().Piece(sq.lib())
	if pc == lib.NoPiece {
		return Piece{}
	}
	return Piece{kindOf[pc.Type()], colourOf(pc.Color())}
}

// Turn is the side to move.
func (p *Position) Turn() Colour { return colourOf(p.pos.Turn()) }

// FEN is the position in Forsyth-Edwards notation, as a sync message carries it.
func (p *Position) FEN() string { return p.pos.String() }

// king is where a side's king stands.
func (p *Position) king(c Colour) (Square, bool) {
	for sq, pc := range p.pieces() {
		if pc == (Piece{'K', c}) {
			return sq, true
		}
	}
	return Square{}, false
}

// attacksKing reports whether the side to move could take the other side's king: whether the side that just
// moved left itself in check.
func (p *Position) attacksKing() bool {
	k, ok := p.king(p.Turn().Other())
	return ok && p.attacked(k, p.Turn())
}

// InCheck reports whether the side to move is in check.
func (p *Position) InCheck() bool {
	k, ok := p.king(p.Turn())
	return ok && p.attacked(k, p.Turn().Other())
}

// attacked reports whether a side's pieces attack a square, worked out from the board alone so it can judge
// a position before the rules are trusted with it.
func (p *Position) attacked(sq Square, by Colour) bool {
	is := func(f, r int, kinds string) bool {
		pc := p.At(Square{f, r})
		return pc.Colour == by && strings.IndexByte(kinds, pc.Kind) >= 0
	}
	forward := 1 // a white pawn attacks from the rank below
	if by == Black {
		forward = -1
	}
	if is(sq.File-1, sq.Rank-forward, "P") || is(sq.File+1, sq.Rank-forward, "P") {
		return true
	}
	for _, d := range [][2]int{{1, 2}, {2, 1}, {2, -1}, {1, -2}, {-1, -2}, {-2, -1}, {-2, 1}, {-1, 2}} {
		if is(sq.File+d[0], sq.Rank+d[1], "N") {
			return true
		}
	}
	for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
		if is(sq.File+d[0], sq.Rank+d[1], "K") {
			return true
		}
		sliders := "RQ"
		if d[0] != 0 && d[1] != 0 {
			sliders = "BQ"
		}
		for f, r := sq.File+d[0], sq.Rank+d[1]; (Square{f, r}).valid(); f, r = f+d[0], r+d[1] {
			if pc := p.At(Square{f, r}); pc != (Piece{}) {
				if is(f, r, sliders) {
					return true
				}
				break
			}
		}
	}
	return false
}

// Move is a legal move.
type Move struct {
	From, To Square
	Promo    byte // Q, R, B or N when a pawn promotes, or 0
	SAN      string
}

// Moves lists the legal moves.
func (p *Position) Moves() []Move {
	var out []Move
	guard(func() error {
		for _, m := range p.pos.ValidMoves() {
			out = append(out, Move{From: fromLib(m.S1()), To: fromLib(m.S2()), Promo: kindOf[m.Promo()],
				SAN: lib.AlgebraicNotation{}.Encode(p.pos, &m)})
		}
		return nil
	})
	return out
}

// From lists the legal moves of the piece on a square.
func (p *Position) From(sq Square) []Move {
	var out []Move
	for _, m := range p.Moves() {
		if m.From == sq {
			out = append(out, m)
		}
	}
	return out
}

// Find is the legal move written as text, in SAN (Nf3, exd8=Q+, O-O) or as squares (g1f3, e7e8q). Check and mate
// marks are optional, and a promotion with no piece named is refused, as it's ambiguous.
func (p *Position) Find(text string) (Move, error) {
	t := strings.TrimRight(strings.TrimSpace(text), "+#!?")
	if t == "" {
		return Move{}, errors.New("chess: no move given")
	}
	moves := p.Moves()
	san := func(s string) string { return strings.TrimRight(s, "+#") }
	for _, m := range moves {
		if san(m.SAN) == t || (t == "0-0" && san(m.SAN) == "O-O") || (t == "0-0-0" && san(m.SAN) == "O-O-O") {
			return m, nil
		}
	}
	if len(t) == 4 || len(t) == 5 {
		from, to := parseSquare(t[0:2]), parseSquare(t[2:4])
		promo := byte(0)
		if len(t) == 5 {
			promo = strings.ToUpper(t[4:5])[0]
		}
		for _, m := range moves {
			if m.From == from && m.To == to && m.Promo == promo {
				return m, nil
			}
		}
	}
	return Move{}, fmt.Errorf("chess: %s isn't a legal move here", text)
}

// parseSquare reads a square like e4, or returns one that isn't on the board.
func parseSquare(s string) Square {
	if len(s) != 2 {
		return Square{-1, -1}
	}
	return Square{int(s[0]) - 'a', int(s[1]) - '1'}
}

// Play is the position after a legal move.
func (p *Position) Play(m Move) (*Position, error) {
	var next *Position
	err := guard(func() error {
		for _, lm := range p.pos.ValidMoves() {
			if fromLib(lm.S1()) == m.From && fromLib(lm.S2()) == m.To && kindOf[lm.Promo()] == m.Promo {
				next = &Position{p.pos.Update(&lm)}
				return nil
			}
		}
		return errors.New("chess: that move isn't legal here")
	})
	return next, err
}

// Outcome is how a position ends the game by the rules, or NoOutcome.
type Outcome int

const (
	NoOutcome Outcome = iota
	Checkmate
	Stalemate
	InsufficientMaterial
	FiftyMoves
)

// Outcome reports whether the side to move is mated or stalemated, neither side can mate, or fifty moves have
// passed without a capture or a pawn moving. Repetition needs the game's history, so Game checks that.
func (p *Position) Outcome() Outcome {
	if len(p.Moves()) == 0 {
		if p.InCheck() {
			return Checkmate
		}
		return Stalemate
	}
	if p.insufficient() {
		return InsufficientMaterial
	}
	if p.pos.HalfMoveClock() >= 100 {
		return FiftyMoves
	}
	return NoOutcome
}

// insufficient is chess.js's test: king against king, a lone knight or bishop, or only bishops on one colour.
func (p *Position) insufficient() bool {
	var others []Piece
	bishopSquares := map[int]bool{}
	for sq, pc := range p.pieces() {
		if pc.Kind == 'K' {
			continue
		}
		others = append(others, pc)
		if pc.Kind == 'B' {
			bishopSquares[(sq.File+sq.Rank)%2] = true
		}
	}
	switch {
	case len(others) == 0:
		return true
	case len(others) == 1:
		return others[0].Kind == 'N' || others[0].Kind == 'B'
	}
	for _, pc := range others {
		if pc.Kind != 'B' {
			return false
		}
	}
	return len(bishopSquares) == 1
}

// repetitionKey is what makes two positions the same for repetition: everything but the move counters.
func (p *Position) repetitionKey() string {
	f := strings.Fields(p.FEN())
	return strings.Join(f[:4], " ")
}

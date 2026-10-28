// Package chess is chess over direct messages, in the format meshcore-companion uses (bridge/app/chess.py and
// pwa/static/app.js in github.com/edleeman17/meshcore-companion). MeshCore has no game packet, so a game is
// ordinary short DMs any client can read, and every move gets the usual delivery receipt:
//
//	♟g3k new white        a challenge: the sender plays white (or black)
//	♟g3k 12. Nf3 #a41c    a move (12... for black's) and a checksum of the position after it
//	♟g3k resign | ♟g3k draw? | ♟g3k draw ok
//	♟g3k sync <FEN>       the sender's whole position, for when the checksums disagree
//
// Nothing is kept but the messages: a game is rebuilt from them each time, as meshcore-companion does, so both
// ends agree on it from the same history.
package chess

import (
	"crypto/rand"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
)

// Prefix starts every chess message.
const Prefix = "♟"

var msgRE = regexp.MustCompile(`^♟[\x{FE0E}\x{FE0F}]?([0-9a-z]{3}) (?:new (white|black)|(\d{1,3})(\.\.\.|\.) (\S{2,8}) #([0-9a-f]{4})` +
	`|(resign|draw\?|draw ok)|sync (\S+ [wb] \S+ \S+ \d+ \d+))$`)

// Kind is what a chess message does.
type Kind int

const (
	KindNew Kind = iota + 1
	KindMove
	KindResign
	KindDrawOffer
	KindDrawAccept
	KindSync
)

// Colour is a side.
type Colour int

const (
	NoColour Colour = iota
	White
	Black
)

func (c Colour) String() string {
	switch c {
	case White:
		return "white"
	case Black:
		return "black"
	}
	return ""
}

// Other is the opponent's side.
func (c Colour) Other() Colour {
	switch c {
	case White:
		return Black
	case Black:
		return White
	}
	return NoColour
}

// Msg is one chess message.
type Msg struct {
	Game   string
	Kind   Kind
	Colour Colour // KindNew: the colour the sender plays
	Ply    int    // KindMove: 0 is white's first move
	SAN    string // KindMove
	Check  string // KindMove: Checksum of the position after the move
	FEN    string // KindSync
}

// Parse reads a chess message, or reports that text isn't one.
func Parse(text string) (Msg, bool) {
	m := msgRE.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return Msg{}, false
	}
	game, colour, num, dots, san, check, act, fen := m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8]
	switch {
	case colour != "":
		c := White
		if colour == "black" {
			c = Black
		}
		return Msg{Game: game, Kind: KindNew, Colour: c}, true
	case san != "":
		n, _ := strconv.Atoi(num) // the pattern allows only 1 to 3 digits
		if n < 1 {
			return Msg{}, false
		}
		ply := (n - 1) * 2
		if dots == "..." {
			ply++
		}
		return Msg{Game: game, Kind: KindMove, Ply: ply, SAN: san, Check: check}, true
	case act != "":
		k := map[string]Kind{"resign": KindResign, "draw?": KindDrawOffer, "draw ok": KindDrawAccept}[act]
		return Msg{Game: game, Kind: k}, true
	}
	return Msg{Game: game, Kind: KindSync, FEN: fen}, true
}

// Text is the message as it's sent.
func (m Msg) Text() string {
	p := Prefix + m.Game + " "
	switch m.Kind {
	case KindNew:
		return p + "new " + m.Colour.String()
	case KindMove:
		return p + Label(m.Ply, m.SAN) + " #" + m.Check
	case KindResign:
		return p + "resign"
	case KindDrawOffer:
		return p + "draw?"
	case KindDrawAccept:
		return p + "draw ok"
	case KindSync:
		return p + "sync " + m.FEN
	}
	return ""
}

// Label names a move as the move list does: "12. Nf3" for white's, "12... Nf6" for black's.
func Label(ply int, san string) string {
	dots := "."
	if ply%2 == 1 {
		dots = "..."
	}
	return fmt.Sprintf("%d%s %s", ply/2+1, dots, san)
}

// Checksum is meshcore-companion's check on a position: the first four hex digits of the FNV-1a hash of its
// board, side to move and castling. En passant is left out, as chess libraries disagree on when to write it.
func Checksum(fen string) string {
	fields := strings.Fields(fen)
	h := fnv.New32a()
	h.Write([]byte(strings.Join(fields[:min(3, len(fields))], " ")))
	return fmt.Sprintf("%08x", h.Sum32())[:4]
}

// NewID makes a game's three-character id.
func NewID() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 3)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// plyOf is how many moves have been played to reach a FEN, from its move number and side to move.
func plyOf(fen string) int {
	f := strings.Fields(fen)
	if len(f) < 6 {
		return 0
	}
	n, err := strconv.Atoi(f[5])
	if err != nil || n < 1 {
		return 0
	}
	ply := (n - 1) * 2
	if f[1] == "b" {
		ply++
	}
	return ply
}

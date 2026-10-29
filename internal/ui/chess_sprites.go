package ui

import (
	"math"
	"math/rand"
	"strings"

	"github.com/verdantran/meshcore-tui/internal/chess"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// Pieces on the bigger boards are pixel art. Each cell is split into quarters by the block characters, so a
// 7×3-cell square is 14×6 pixels and a 5×2 one is 10×4; the pixels are twice as tall as they're wide, as cells
// are. Every cell has one colour for the piece and one for the square, which is enough to light each piece from
// above: a highlight on its crown, shadow at its base.

// bigSprites are for 7×3-cell squares: each # is a quarter of a cell.
var bigSprites = map[byte][]string{
	'K': {"......##......", "....######....", "......##......", "...########...", "....######....", "...########..."},
	'Q': {"..#...##...#..", "..##..##..##..", "...########...", "....######....", ".....####.....", "...########..."},
	'R': {"...##.##.##...", "...########...", "....######....", "....######....", "...########...", "...########..."},
	'B': {".......#......", "......###.....", ".....#.###....", ".....#####....", "......###.....", "...########..."},
	'N': {".......##.....", ".....######...", "...#########..", "...##..#####..", "......######..", "...#########.."},
	'P': {"..............", "......##......", ".....####.....", "......##......", ".....####.....", "....######...."},
}

// smallSprites are the same pieces for 5×2-cell squares.
var smallSprites = map[byte][]string{
	'K': {"....##....", "..######..", "...####...", "..######.."},
	'Q': {".#..##..#.", ".########.", "..######..", ".########."},
	'R': {".##.##.##.", "..######..", "..######..", ".########."},
	'B': {"....#.....", "...#.##...", "....##....", "..######.."},
	'N': {"....###...", "..######..", ".##..###..", "..######.."},
	'P': {"....##....", "...####...", "....##....", "..######.."},
}

// spritesFor is the sprite set for a square size, or nil for one too small for art, which keeps the symbols.
func spritesFor(cw, ch int) map[byte][]string {
	switch {
	case cw == 7 && ch == 3:
		return bigSprites
	case cw == 5 && ch == 2:
		return smallSprites
	}
	return nil
}

// quarters are the block characters for a cell's four quarters, indexed by top left, top right, bottom left
// and bottom right as bits 1, 2, 4 and 8.
var quarters = []string{" ", "▘", "▝", "▀", "▖", "▌", "▞", "▛", "▗", "▚", "▐", "▜", "▄", "▙", "▟", "█"}

// Ivory and ink, the classic set: light to dark, from a piece's crown to its base.
var (
	ivory = [3]theme.RGB{theme.Hex("#fffaf0"), theme.Hex("#d9cbb2"), theme.Hex("#a8946f")}
	ink   = [3]theme.RGB{theme.Hex("#2e2342"), theme.Hex("#1a1128"), theme.Hex("#0a0612")}
)

// squareColours are the board's light and dark squares: mid-tones, tinted by the theme, so both ivory and ink
// stand out on both.
func squareColours(th theme.Theme) (light, dark theme.RGB) {
	return theme.Hex("#8f8a99").Lerp(th.Violet, 0.3), theme.Hex("#2b2238").Lerp(th.Violet, 0.2)
}

// lit is a piece's colour in one cell of its sprite: lighter towards the top, and on the left, where the light
// comes from.
func lit(side chess.Colour, row, rows, col, cols int) theme.RGB {
	p := ivory
	if side == chess.Black {
		p = ink
	}
	t := 0.5
	if rows > 1 {
		t = float64(row) / float64(rows-1)
	}
	base := p[0].Lerp(p[1], t*2)
	if t > 0.5 {
		base = p[1].Lerp(p[2], (t-0.5)*2)
	}
	lean := 0.5
	if cols > 1 {
		lean = float64(col) / float64(cols-1)
	}
	switch {
	case lean < 0.45:
		return base.Lerp(p[0], 0.25)
	case lean > 0.7:
		return base.Lerp(p[2], 0.25)
	}
	return base
}

// pixel is one quarter of a cell: lit by a piece, in its colour, or not.
type pixel struct {
	on  bool
	col theme.RGB
}

// spriteLines draws the board with pixel-art pieces. A move glides its whole sprite across, a pixel at a time;
// a capture breaks up as the mover lands; a lifted piece rises a pixel; a lost king breaks up when the game ends.
func (m *Model) spriteLines(cw, ch int, sprites map[byte][]string) []string {
	th, b, now := m.th, m.chats.board, m.now
	g := m.boardGame()
	pos := g.Pos
	last, hasLast := g.Last()
	targets := map[chess.Square]bool{}
	if b.picked {
		for _, mv := range pos.From(b.from) {
			targets[mv.To] = true
		}
	}
	check := pos.InCheck()
	gliding := hasLast && m.level.Allows(motion.Wipe) && motion.Running(b.moveAt, slideDur, now) && b.mover != (chess.Piece{})
	settled := motion.Progress(b.moveAt.Add(slideDur), flashDur, now)
	if !m.level.Allows(motion.Wipe) {
		settled = motion.Progress(b.moveAt, flashDur, now)
	}
	flashing := hasLast && m.level.Allows(motion.Flash) && settled < 1
	revealed := 8.0
	if m.level.Allows(motion.Wipe) {
		revealed = 8 * motion.EaseOut(motion.Progress(b.openedAt, openDur, now))
	}
	ended := g.End != nil && m.level.Allows(motion.Glitch) && motion.Running(b.endAt, endGlitch, now)
	light, dark := squareColours(th)

	screen := func(sq chess.Square) (int, int) {
		if b.viewer == chess.Black {
			return sq.Rank, 7 - sq.File
		}
		return 7 - sq.Rank, sq.File
	}
	rows, cols := 8*ch, 8*cw
	bg := make([][]theme.RGB, rows)
	text := make([][]string, rows) // a cell drawn as a character rather than pixels: a move's target dot
	pix := make([][]pixel, 2*rows)
	for r := range bg {
		bg[r], text[r] = make([]theme.RGB, cols), make([]string, cols)
	}
	for y := range pix {
		pix[y] = make([]pixel, 2*cols)
	}

	// stamp draws a sprite with its top-left pixel at (x0, y0), its colour lit by where each pixel is in the
	// sprite. keep, if given, says which of its pixels show, for a piece breaking up.
	stamp := func(pc chess.Piece, x0, y0 int, tint theme.RGB, tinted float64, keep func(x, y int) bool) {
		s := sprites[pc.Kind]
		hollow := th.Mono && pc.Colour == chess.White // without colour, white pieces are outlines
		for y, line := range s {
			for x := range len(line) {
				if line[x] != '#' || hollow && interior(s, x, y) || keep != nil && !keep(x, y) {
					continue
				}
				px, py := x0+x, y0+y
				if px < 0 || py < 0 || px >= 2*cols || py >= 2*rows {
					continue
				}
				col := lit(pc.Colour, y/2, ch, x/2, cw)
				if tinted > 0 {
					col = col.Lerp(tint, tinted)
				}
				pix[py][px] = pixel{on: true, col: col}
			}
		}
	}

	for file := range 8 {
		for rank := range 8 {
			sq := chess.Square{File: file, Rank: rank}
			row, col := screen(sq)
			pc := pos.At(sq)
			base := light
			if (file+rank)%2 == 0 {
				base = dark
			}
			sbg := base
			if hasLast && (sq == last.From || sq == last.To) {
				sbg = th.Yellow.Lerp(base, 0.55)
				if flashing && !gliding {
					sbg = th.Yellow.Lerp(base, 0.1+0.45*settled)
				}
			}
			if check && pc.Kind == 'K' && pc.Colour == pos.Turn() {
				glow := 0.3
				if m.level.Allows(motion.Flash) {
					glow = 0.2 + 0.4*motion.Pulse(now, pulsePeriod)
				}
				sbg = th.Red.Lerp(base, 1-glow-0.3)
			}
			if b.picked && sq == b.from {
				p := 1.0
				if m.level.Allows(motion.Flash) {
					p = motion.EaseOut(motion.Progress(b.pickedAt, popDur, now))
				}
				sbg = th.Magenta.Lerp(base, 1-0.4*p)
			}
			if targets[sq] && pc != (chess.Piece{}) {
				sbg = th.Green.Lerp(base, 0.45)
			}
			if sq == b.cursor && m.boardFocused() {
				sbg = th.Cyan.Lerp(base, 0.35)
			}
			for dy := range ch {
				for dx := range cw {
					bg[row*ch+dy][col*cw+dx] = sbg
				}
			}
			x0, y0 := 2*col*cw, 2*row*ch
			switch {
			case gliding && sq == last.To:
				if b.taken != (chess.Piece{}) && m.level.Allows(motion.Glitch) {
					t := motion.Progress(b.moveAt, slideDur, now)
					seed := b.moveAt.UnixNano()
					stamp(b.taken, x0, y0, th.Red, 0.5, func(x, y int) bool { return breakUp(seed, x, y) > t })
				}
			case pc != (chess.Piece{}):
				lift := 0
				if b.picked && sq == b.from {
					lift = 1 // lifted off the square
				}
				var keep func(x, y int) bool
				tinted := 0.0
				if ended && pc.Kind == 'K' && m.lost(g, pc.Colour) {
					frame := int(now.Sub(b.endAt) / motion.FrameDur)
					seed := b.endAt.UnixNano() + int64(frame)*7919
					keep, tinted = func(x, y int) bool { return breakUp(seed, x, y) > 0.35 }, 0.6
				}
				stamp(pc, x0, y0-lift, th.Red, tinted, keep)
			case targets[sq]:
				text[row*ch+ch/2][col*cw+cw/2] = "·"
			}
		}
	}
	if gliding {
		// The moving piece, part way from its square to the one it's going to, a pixel at a time.
		t := motion.EaseOut(motion.Progress(b.moveAt, slideDur, now))
		fr, fc := screen(last.From)
		tr, tc := screen(last.To)
		y := int(math.Round((float64(fr) + float64(tr-fr)*t) * float64(2*ch)))
		x := int(math.Round((float64(fc) + float64(tc-fc)*t) * float64(2*cw)))
		stamp(b.mover, x, y, theme.RGB{}, 0, nil)
	}

	var out []string
	for r := range rows {
		label := "   "
		if r%ch == ch/2 {
			rank := 8 - r/ch
			if b.viewer == chess.Black {
				rank = r/ch + 1
			}
			label = th.Paint(th.Dim, false, " "+string(rune('0'+rank))+" ")
		}
		if float64(8-r/ch) > revealed+0.5 {
			out = append(out, label+strings.Repeat(" ", cols))
			continue
		}
		var line strings.Builder
		line.WriteString(label)
		for c := range cols {
			if t := text[r][c]; t != "" {
				line.WriteString(paintCell(th, cell{s: t, fg: th.Green, bg: bg[r][c]}, false))
				continue
			}
			bits, fg := 0, th.Text
			for i, q := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				if p := pix[2*r+q[1]][2*c+q[0]]; p.on {
					bits |= 1 << i
					fg = p.col
				}
			}
			cursor := th.Mono && m.boardFocused() && b.cursor == m.squareAtCell(r, c, cw, ch)
			line.WriteString(paintCell(th, cell{s: quarters[bits], fg: fg, bg: bg[r][c]}, cursor))
		}
		out = append(out, line.String())
	}
	files := "   "
	for col := range 8 {
		f := col
		if b.viewer == chess.Black {
			f = 7 - col
		}
		files += strings.Repeat(" ", cw/2) + string(rune('a'+f)) + strings.Repeat(" ", cw-cw/2-1)
	}
	return append(out, th.Paint(th.Dim, false, files))
}

// interior reports whether a sprite's pixel has its sprite on all four sides, so an outline leaves it out.
func interior(s []string, x, y int) bool {
	on := func(x, y int) bool { return y >= 0 && y < len(s) && x >= 0 && x < len(s[y]) && s[y][x] == '#' }
	return on(x-1, y) && on(x+1, y) && on(x, y-1) && on(x, y+1)
}

// breakUp is a pixel's place in a piece breaking up: a pixel shows while it's above how far the break-up has
// gone, so a piece falls apart in a scatter rather than from one side.
func breakUp(seed int64, x, y int) float64 {
	return rand.New(rand.NewSource(seed + int64(y)*131 + int64(x)*7)).Float64()
}

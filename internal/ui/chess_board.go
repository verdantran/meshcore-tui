package ui

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/chess"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// boardView is a chess game open in a direct conversation, played with its contact. The game is rebuilt from
// the conversation's chess messages each time it changes; nothing is kept but where you're looking and what's
// animating.
type boardView struct {
	convo  store.Convo
	game   *chess.Game
	fresh  bool                    // the board is set up for a new game, which your first move starts
	msgs   map[int64]store.Message // the conversation's chess messages, by id, for their delivery state
	cursor chess.Square
	picked bool         // a piece is lifted, from: where it stands
	from   chess.Square //
	promo  []chess.Move // the choice of piece while a pawn promotes
	choice int
	typing bool // a move is being typed
	input  lineEdit
	flip   bool
	chat   bool // the conversation beside the board has the keys
	idle   bool // neither does: esc left them with the list, to choose another conversation
	colour bool // choosing a colour for a new game
	note   string

	// Where the board was last drawn, in the conversation pane, for clicks.
	at     [2]int // the cell of its top-left square
	cw, ch int    // a square's size in cells
	drawn  bool
	viewer chess.Colour // the side at the bottom

	// Animation: when things happened, all checked against the frame clock.
	openedAt time.Time
	moveAt   time.Time // the latest move arriving, or being made
	mover    chess.Piece
	taken    chess.Piece // what the latest move captured, glitching out as the mover lands
	pickedAt time.Time
	endAt    time.Time
	status   string // the status line as last shown, to decode a new one in
	statusAt time.Time
}

const (
	chatBesideW = 30 // the least the conversation needs beside the board
	flashDur    = 700 * time.Millisecond
	openDur     = 3 * wipeDur
	endGlitch   = 6 * glitchDur
)

// Square sizes in cells, biggest first; the board takes the biggest that fits.
var squareSizes = [][2]int{{7, 3}, {5, 2}, {3, 1}}

// openBoard opens chess in the selected conversation, if it's with a person.
func (m *Model) openBoard() {
	cur, ok := m.selectedConvo()
	if !ok || m.chat == nil {
		return
	}
	ct, isContact := m.chat.Contact(cur.Convo)
	if cur.Kind != chat.KindDirect || !isContact || ct.Type != radio.TypeChat {
		m.addLog(m.now, "CHESS  is only for direct messages with a person", radio.ToneInfo)
		return
	}
	m.chats.reading, m.chats.writing = false, false
	m.chats.board = &boardView{convo: cur.Convo, openedAt: m.now}
	m.refreshBoard()
	b := m.chats.board
	b.cursor = chess.Square{File: 4, Rank: 1}
	if g := m.boardGame(); g.Mine == chess.Black {
		b.cursor = chess.Square{File: 4, Rank: 6}
	}
}

func (m *Model) closeBoard() { m.chats.board = nil }

// boardShown reports chess open in the conversation that's selected.
func (m *Model) boardShown() bool {
	b := m.chats.board
	if b == nil {
		return false
	}
	cur, ok := m.selectedConvo()
	return ok && cur.Convo == b.convo
}

// boardFocused reports the board itself having the keys.
func (m *Model) boardFocused() bool {
	return m.boardShown() && !m.chats.board.chat && !m.chats.board.idle
}

// refreshBoard rebuilds the game from the conversation's chess messages, and starts the animations for what's
// changed since: a move gliding in, a game ending.
func (m *Model) refreshBoard() {
	b := m.chats.board
	if b == nil || m.chat == nil {
		return
	}
	if !m.boardShown() {
		return // it stays open in its conversation while you look at another
	}
	msgs, err := m.chat.ChessMessages(b.convo)
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
		return
	}
	b.msgs = map[int64]store.Message{}
	entries := make([]chess.Entry, 0, len(msgs))
	for _, msg := range msgs {
		at := msg.RecvAt
		if msg.Out {
			at = msg.SentAt
		}
		b.msgs[msg.ID] = msg
		entries = append(entries, chess.Entry{Text: msg.Text, Mine: msg.Out, At: at, Failed: msg.State == store.Failed, Ref: msg.ID})
	}
	prev, g := b.game, chess.Latest(entries)
	b.game = g
	switch {
	case g == nil:
		b.fresh = true
	case g.End == nil:
		b.fresh = false
	}
	if prev != nil && g != nil && prev.ID == g.ID && g.Ply == prev.Ply+1 {
		if last, ok := g.Last(); ok {
			b.moveAt, b.mover, b.taken = m.now, prev.Pos.At(last.From), prev.Pos.At(last.To)
		}
	}
	if prev != nil && g != nil && g.End != nil && (prev.End == nil || prev.ID != g.ID) {
		b.endAt = m.now
	}
	if gg := m.boardGame(); b.picked && (!gg.MyTurn() || len(gg.Pos.From(b.from)) == 0) {
		b.picked, b.promo = false, nil
	}
	m.noteStatus()
}

// noteStatus starts the status line decoding in when what it says has changed.
func (m *Model) noteStatus() {
	b := m.chats.board
	if b == nil {
		return
	}
	if text, _, _ := m.statusLine(); text != b.status {
		if b.status != "" {
			b.statusAt = m.now
		}
		b.status = text
	}
}

// boardGame is the game the board plays: the one in the conversation, or a new one set up for your first move.
func (m *Model) boardGame() *chess.Game {
	b := m.chats.board
	if b.fresh || b.game == nil {
		return &chess.Game{Mine: chess.White, Pos: chess.Start(), Gap: -1}
	}
	return b.game
}

func (m *Model) chessLayout() string {
	layout := m.chats.boardLayout
	if layout == "" && m.opt.Config != nil {
		layout = m.opt.Config.UI.ChessLayout
	}
	if layout == "board" {
		return "board"
	}
	return "both"
}

func (m *Model) toggleChessLayout() {
	next := "board"
	if m.chessLayout() == "board" {
		next = "both"
	}
	m.chats.boardLayout = next
	if m.opt.Config != nil {
		m.opt.Config.UI.ChessLayout = next
		m.saveConfig()
	}
	m.chats.board.chat = false
}

// boardKey handles a key while the board has them; one it doesn't use goes on to the screen and the app.
func (m *Model) boardKey(s string) (tea.Cmd, bool) {
	defer m.noteStatus()
	b := m.chats.board
	b.note = ""
	switch {
	case b.typing:
		switch s {
		case "esc":
			b.typing = false
		case "enter":
			return m.playTyped(), true
		default:
			b.input.key(s)
		}
		return nil, true
	case b.promo != nil:
		switch s {
		case "left", "h":
			b.choice = (b.choice + len(b.promo) - 1) % len(b.promo)
		case "right", "l":
			b.choice = (b.choice + 1) % len(b.promo)
		case "enter", "space":
			return m.playMove(b.promo[b.choice]), true
		case "q", "r", "b", "n":
			for _, mv := range b.promo {
				if mv.Promo == strings.ToUpper(s)[0] {
					return m.playMove(mv), true
				}
			}
		case "esc":
			b.promo, b.picked = nil, false
		}
		return nil, true
	case b.colour:
		b.colour = false
		switch s {
		case "w":
			return m.sendChess(chess.Msg{Game: chess.NewID(), Kind: chess.KindNew, Colour: chess.White}), true
		case "b":
			return m.sendChess(chess.Msg{Game: chess.NewID(), Kind: chess.KindNew, Colour: chess.Black}), true
		}
		return nil, true
	}
	g := b.game
	up := 1
	if b.viewer == chess.Black {
		up = -1 // the board is drawn black's way up, so up is towards rank 1
	}
	switch s {
	case "up", "k":
		b.cursor.Rank = clampSq(b.cursor.Rank + up)
	case "down", "j":
		b.cursor.Rank = clampSq(b.cursor.Rank - up)
	case "left", "h":
		b.cursor.File = clampSq(b.cursor.File - up)
	case "right", "l":
		b.cursor.File = clampSq(b.cursor.File + up)
	case "enter", "space":
		return m.pressSquare(b.cursor), true
	case "esc":
		if b.picked {
			b.picked = false
		} else {
			b.idle = true // the list has the keys, with the board left open
		}
	case "c":
		m.closeBoard() // c opened it, and closes it again
	case "f":
		b.flip = !b.flip
	case "i":
		m.setUpAfterEnd()
		if !m.boardGame().MyTurn() {
			b.note = m.notYourMove()
		} else {
			b.typing, b.input, b.picked = true, lineEdit{}, false
		}
	case "n":
		if g != nil && g.End == nil && g.Mine != chess.NoColour {
			b.note = "Finish this game first: d offers a draw, R resigns"
		} else {
			b.colour = true
		}
	case "d":
		return m.drawKey(), true
	case "R":
		if g == nil || g.End != nil || g.Mine == chess.NoColour {
			b.note = "There's no game going to resign"
			return nil, true
		}
		them := m.chat.Title(b.convo)
		id := g.ID
		m.ask("Resign this game? "+them+" wins", "resign", func() tea.Cmd {
			return m.sendChess(chess.Msg{Game: id, Kind: chess.KindResign})
		})
	case "S":
		// Not only when the boards disagree: their side can lose the game while yours still looks fine (a
		// new device, a cleared database), and the position is how it catches up.
		if g == nil || g.Mine == chess.NoColour || g.End != nil {
			b.note = "There's no game going to send"
			return nil, true
		}
		id, fen := g.ID, g.Pos.FEN()
		what := "Send your position? Their board is replaced with yours"
		if g.Problem == nil {
			what = "Send your whole position? Their board is replaced with yours, for a device that missed moves"
		}
		m.ask(what, "send it", func() tea.Cmd {
			return m.sendChess(chess.Msg{Game: id, Kind: chess.KindSync, FEN: fen})
		})
	case "r":
		if msg, ok := m.failedMove(); ok {
			return m.retry(msg), true
		}
		// A receipt only says their radio got it: their app can still lose it, so send it again on request.
		p, ok := m.lastMove()
		if !ok || g.End != nil {
			b.note = "No move of yours to send again"
			return nil, true
		}
		mv := chess.Msg{Game: g.ID, Kind: chess.KindMove, Ply: p.Ply, SAN: p.SAN, Check: p.Check}
		m.ask("Send "+p.Label()+" again? It got a receipt, so only if their side lost it", "send it", func() tea.Cmd {
			return m.sendChess(mv)
		})
	case "L":
		m.toggleChessLayout()
	case "tab":
		if m.chessLayout() == "both" {
			b.chat = true
		}
	default:
		return nil, false
	}
	return nil, true
}

func clampSq(n int) int { return min(max(n, 0), 7) }

// setUpAfterEnd sets the board up for a new game once the last one's over, on the first move towards one.
func (m *Model) setUpAfterEnd() {
	b := m.chats.board
	if b.game != nil && b.game.End != nil && !b.fresh {
		b.fresh, b.picked, b.moveAt, b.openedAt = true, false, time.Time{}, m.now
		b.note = "A new game: you're white, so make the first move · n then b to play black"
	}
}

func (m *Model) notYourMove() string {
	g := m.boardGame()
	switch {
	case g.End != nil:
		return "This game is over: any move starts another"
	case g.Problem != nil:
		return "Your boards don't match: S sends your position"
	case g.Gap >= 0:
		return "Waiting for a move that hasn't arrived"
	case g.Mine == chess.NoColour:
		return "Their challenge is missing, so it isn't clear which side you are"
	}
	return "It's " + m.chat.Title(m.chats.board.convo) + "'s move"
}

// pressSquare lifts one of your pieces from a square, or puts the lifted one down there.
func (m *Model) pressSquare(sq chess.Square) tea.Cmd {
	b := m.chats.board
	b.cursor = sq
	if b.game != nil && b.game.End != nil && !b.fresh {
		m.setUpAfterEnd()
		return nil
	}
	g := m.boardGame()
	if !g.MyTurn() {
		b.note = m.notYourMove()
		return nil
	}
	if pc := g.Pos.At(sq); pc.Colour == g.Mine {
		if len(g.Pos.From(sq)) == 0 {
			b.picked, b.note = false, "That piece has nowhere to go"
			return nil
		}
		b.picked, b.from, b.pickedAt = true, sq, m.now
		return nil
	}
	if !b.picked {
		return nil
	}
	var moves []chess.Move
	for _, mv := range g.Pos.From(b.from) {
		if mv.To == sq {
			moves = append(moves, mv)
		}
	}
	switch {
	case len(moves) == 0:
		b.picked = false
		return nil
	case len(moves) > 1:
		b.promo, b.choice = promoOrder(moves), 0
		return nil
	}
	return m.playMove(moves[0])
}

// promoOrder offers a promotion queen first, as it's nearly always the one.
func promoOrder(moves []chess.Move) []chess.Move {
	var out []chess.Move
	for _, k := range "QRBN" {
		for _, mv := range moves {
			if rune(mv.Promo) == k {
				out = append(out, mv)
			}
		}
	}
	return out
}

func (m *Model) playTyped() tea.Cmd {
	b := m.chats.board
	mv, err := m.boardGame().Pos.Find(b.input.text)
	if err != nil {
		b.note = strings.TrimPrefix(err.Error(), "chess: ")
		return nil
	}
	b.typing = false
	return m.playMove(mv)
}

// playMove sends a move. On a board set up for a new game it sends the challenge first, so making the first
// move is all it takes to start one.
func (m *Model) playMove(mv chess.Move) tea.Cmd {
	b := m.chats.board
	b.picked, b.promo = false, nil
	g := m.boardGame()
	var challenge tea.Cmd
	if b.fresh {
		g = &chess.Game{ID: chess.NewID(), Mine: chess.White, Pos: chess.Start(), Gap: -1}
		challenge = m.sendChess(chess.Msg{Game: g.ID, Kind: chess.KindNew, Colour: chess.White})
	}
	msg, err := g.Next(mv)
	if err != nil {
		b.note = strings.TrimPrefix(err.Error(), "chess: ")
		return challenge
	}
	b.cursor = mv.To
	b.moveAt, b.mover, b.taken = m.now, g.Pos.At(mv.From), g.Pos.At(mv.To)
	move := m.sendChess(msg)
	b.moveAt = m.now // sending rebuilt the game, which would have started the same glide
	if challenge != nil {
		return inOrder(challenge, move) // so the challenge goes out first
	}
	return move
}

// inOrder makes radio calls one after another, then hands back each one's result.
func inOrder(cmds ...tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		var out tea.BatchMsg
		for _, c := range cmds {
			if c == nil {
				continue
			}
			msg := c()
			out = append(out, func() tea.Msg { return msg })
		}
		return out
	}
}

func (m *Model) drawKey() tea.Cmd {
	b := m.chats.board
	g := b.game
	switch {
	case g == nil || g.End != nil || g.Mine == chess.NoColour || b.fresh:
		b.note = "There's no game going"
	case g.Offer != nil && !g.Offer.Mine:
		return m.sendChess(chess.Msg{Game: g.ID, Kind: chess.KindDrawAccept})
	case g.Offer != nil:
		b.note = "You've offered a draw: waiting for " + m.chat.Title(b.convo)
	default:
		return m.sendChess(chess.Msg{Game: g.ID, Kind: chess.KindDrawOffer})
	}
	return nil
}

// sendChess sends a chess message as an ordinary direct message, with its usual retries and receipt.
func (m *Model) sendChess(msg chess.Msg) tea.Cmd {
	b := m.chats.board
	_, send, err := m.chat.Compose(b.convo, msg.Text(), m.now)
	if err != nil {
		m.fail("CHESS  " + err.Error())
		return nil
	}
	m.refreshChats()
	return m.sendCmd(send)
}

// lastMove is your latest move in the game on the board.
func (m *Model) lastMove() (chess.Played, bool) {
	b := m.chats.board
	if b.game == nil {
		return chess.Played{}, false
	}
	for i := len(b.game.Played) - 1; i >= 0; i-- {
		if p := b.game.Played[i]; p.Mine {
			return p, true
		}
	}
	return chess.Played{}, false
}

// failedMove is your latest move, if it never got a receipt.
func (m *Model) failedMove() (store.Message, bool) {
	b := m.chats.board
	if b.game == nil {
		return store.Message{}, false
	}
	for i := len(b.game.Played) - 1; i >= 0; i-- {
		if p := b.game.Played[i]; p.Mine {
			msg, ok := b.msgs[p.Entry.Ref]
			return msg, ok && msg.State == store.Failed
		}
	}
	return store.Message{}, false
}

// boardAnimating reports a chess animation running, for the frame clock.
func (m *Model) boardAnimating() bool {
	b := m.chats.board
	if !m.boardShown() || !m.visible(0) || m.level == motion.Off {
		return false
	}
	now := m.now
	return motion.Running(b.openedAt, openDur, now) || motion.Running(b.moveAt, slideDur+flashDur, now) ||
		motion.Running(b.pickedAt, popDur, now) || motion.Running(b.endAt, endGlitch, now) ||
		motion.Running(b.statusAt, decodeDur, now)
}

// boardPulsing is a king in check, glowing while it waits on its side to answer.
func (m *Model) boardPulsing() bool {
	if !m.boardShown() || !m.visible(0) || !m.level.Allows(motion.Flash) {
		return false
	}
	g := m.boardGame()
	return g.End == nil && g.Pos.InCheck()
}

// boardRows is how many rows the board block needs for squares ch cells tall: the title, the board and its file
// letters, the status under it and the keys.
func boardRows(ch int) int { return 2 + 8*ch + 1 + 6 + 3 }

func boardWidth(cw int) int { return 3 + 8*cw }

// squareSize is the biggest square that fits a board in w cells by rows.
func squareSize(w, rows int) (int, int, bool) {
	for _, s := range squareSizes {
		if boardWidth(s[0]) <= w && boardRows(s[1]) <= rows {
			return s[0], s[1], true
		}
	}
	return 3, 1, false
}

// boardPane is the conversation pane while chess is open: the board alone, or with the conversation beside it,
// or under it when there's no room beside.
func (m *Model) boardPane(w, rows int) []string {
	b := m.chats.board
	b.drawn = false
	if m.chessLayout() == "both" {
		if cw, ch, ok := squareSize(w-3-chatBesideW, rows); ok {
			bw := max(boardWidth(cw), 32)
			left := m.boardBlock(bw, rows, cw, ch, false)
			right := m.convoPane(w-bw-3, rows)
			if m.boardFocused() && len(right) > 0 {
				// The conversation's own keys don't reach it while the board has them.
				right[len(right)-1] = m.th.Paint(m.th.Dim, false, "tab to write in the chat")
			}
			sep := m.th.Paint(m.th.Line, false, " │ ")
			out := make([]string, rows)
			for i := range out {
				l, r := "", ""
				if i < len(left) {
					l = left[i]
				}
				if i < len(right) {
					r = right[i]
				}
				out[i] = fit(l, bw) + sep + r
			}
			return out
		}
		if cw, ch, ok := squareSize(w, rows-12); ok {
			top := m.boardBlock(w, boardRows(ch), cw, ch, false)
			out := append(top, m.th.Paint(m.th.Line, false, strings.Repeat("─", w)))
			return append(out, m.convoPane(w, rows-len(out))...)
		}
		b.chat = false
	}
	moves := 18 // the move list beside the board
	cw, ch, _ := squareSize(w-moves, rows)
	return m.boardBlock(w, rows, cw, ch, true)
}

// boardBlock is the board with what's going on in the game: alone, it has the move list beside it.
func (m *Model) boardBlock(w, rows, cw, ch int, alone bool) []string {
	th, b := m.th, m.chats.board
	g := m.boardGame()
	them := m.chat.Title(b.convo)
	title := th.Paint(th.Magenta, true, "▌") + th.Paint(th.Text, true, chess.Prefix+" CHESS · "+them)
	side := ""
	if g.Mine != chess.NoColour {
		side = th.Paint(th.Dim, false, "  you play "+g.Mine.String())
	}
	out := []string{clip(title, w), side}
	b.viewer = chess.White
	if g.Mine == chess.Black {
		b.viewer = chess.Black
	}
	if b.flip {
		b.viewer = b.viewer.Other()
	}
	board := m.boardLines(cw, ch)
	b.at, b.cw, b.ch, b.drawn = [2]int{len(out), 3}, cw, ch, true
	var moves []string
	if bw := boardWidth(cw); alone && w >= bw+16 {
		moves = m.moveList(len(board))
		for i, l := range moves {
			moves[i] = strings.Repeat(" ", 3) + l
		}
	}
	for i, l := range board {
		if i < len(moves) {
			l = fit(l, boardWidth(cw)) + moves[i]
		}
		out = append(out, l)
	}
	out = append(out, "")
	for _, l := range m.gameStatus(w) {
		out = append(out, clip(l, w))
	}
	keys := m.boardKeys(w)
	for len(out) < rows-len(keys) {
		out = append(out, "")
	}
	for _, l := range keys {
		out = append(out, th.Paint(th.Dim, false, clip(l, w)))
	}
	return out
}

var (
	whiteGlyph = map[byte]string{'K': "♔", 'Q': "♕", 'R': "♖", 'B': "♗", 'N': "♘", 'P': "♙"}
	blackGlyph = map[byte]string{'K': "♚", 'Q': "♛", 'R': "♜", 'B': "♝", 'N': "♞", 'P': "♟"}
)

func glyph(pc chess.Piece) string {
	if pc.Colour == chess.Black {
		return blackGlyph[pc.Kind]
	}
	return whiteGlyph[pc.Kind]
}

// cell is one character cell of the board.
type cell struct {
	s      string
	fg, bg theme.RGB
}

// boardLines draws the board, the viewer's side at the bottom, with the file letters under it. Squares are cw by
// ch cells, each piece in the middle of its square. A move glides its piece across and flashes the squares it
// joined; a capture glitches out as the mover lands; a king in check glows.
func (m *Model) boardLines(cw, ch int) []string {
	if s := spritesFor(cw, ch); s != nil && !m.opt.ASCII {
		return m.spriteLines(cw, ch, s)
	}
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
	revealed := 8.0 // ranks drawn so far as the board opens, from the viewer's side up
	if m.level.Allows(motion.Wipe) {
		revealed = 8 * motion.EaseOut(motion.Progress(b.openedAt, openDur, now))
	}
	ended := g.End != nil && m.level.Allows(motion.Glitch) && motion.Running(b.endAt, endGlitch, now)

	// screen maps a square to its row and column on screen, from the top left.
	screen := func(sq chess.Square) (int, int) {
		if b.viewer == chess.Black {
			return sq.Rank, 7 - sq.File
		}
		return 7 - sq.Rank, sq.File
	}
	grid := make([][]cell, 8*ch)
	for r := range grid {
		grid[r] = make([]cell, 8*cw)
	}
	for file := range 8 {
		for rank := range 8 {
			sq := chess.Square{File: file, Rank: rank}
			row, col := screen(sq)
			pc := pos.At(sq)
			light, dark := squareColours(th)
			base := dark
			if (file+rank)%2 == 1 {
				base = light
			}
			bg := base
			if hasLast && (sq == last.From || sq == last.To) {
				bg = th.Yellow.Lerp(base, 0.55)
				if flashing && !gliding {
					bg = th.Yellow.Lerp(base, 0.1+0.45*settled) // bright as the piece lands, settling to the tint
				}
			}
			if check && pc.Kind == 'K' && pc.Colour == pos.Turn() {
				glow := 0.3
				if m.level.Allows(motion.Flash) {
					glow = 0.2 + 0.4*motion.Pulse(now, pulsePeriod)
				}
				bg = th.Red.Lerp(base, 1-glow-0.3)
			}
			if b.picked && sq == b.from {
				p := 1.0
				if m.level.Allows(motion.Flash) {
					p = motion.EaseOut(motion.Progress(b.pickedAt, popDur, now))
				}
				bg = th.Magenta.Lerp(base, 0.3*p) // bright as it's lifted, then held
			}
			if targets[sq] && pc != (chess.Piece{}) {
				bg = th.Green.Lerp(base, 0.45)
			}
			cursor := sq == b.cursor && m.boardFocused()
			if cursor {
				bg = th.Cyan.Lerp(base, 0.15)
			}
			text, fg := "", th.Text
			switch {
			case gliding && sq == last.To:
				if b.taken != (chess.Piece{}) && m.level.Allows(motion.Glitch) {
					text, fg = glitchGlyph(glyph(b.taken), b.moveAt.UnixNano(), int(now.Sub(b.moveAt)/motion.FrameDur)), th.Red
				}
			case pc != (chess.Piece{}):
				text = glyph(pc)
				if pc.Colour == chess.Black {
					fg = th.Magenta
				}
				if ended && pc.Kind == 'K' && m.lost(g, pc.Colour) {
					text, fg = glitchGlyph(text, b.endAt.UnixNano(), int(now.Sub(b.endAt)/motion.FrameDur)), th.Red
				}
			case targets[sq]:
				text, fg = "·", th.Green
			}
			if cursor {
				fg = th.Bg
			}
			if th.Mono && (file+rank)%2 == 0 && !cursor {
				bg = theme.RGB{} // marked with ░ instead
			}
			for dy := range ch {
				for dx := range cw {
					c := cell{s: " ", fg: fg, bg: bg}
					if th.Mono && (file+rank)%2 == 0 && !cursor {
						c.s = "░"
					}
					if dy == ch/2 && dx == cw/2 && text != "" {
						c.s = text
					}
					grid[row*ch+dy][col*cw+dx] = c
				}
			}
		}
	}
	if gliding {
		// The moving piece, part way from its square to the one it's going to.
		t := motion.EaseOut(motion.Progress(b.moveAt, slideDur, now))
		fr, fc := screen(last.From)
		tr, tc := screen(last.To)
		y := int(math.Round((float64(fr) + (float64(tr-fr))*t) * float64(ch)))
		x := int(math.Round((float64(fc) + (float64(tc-fc))*t) * float64(cw)))
		y, x = min(max(y+ch/2, 0), 8*ch-1), min(max(x+cw/2, 0), 8*cw-1)
		fg := th.Text
		if b.mover.Colour == chess.Black {
			fg = th.Magenta
		}
		grid[y][x].s, grid[y][x].fg = glyph(b.mover), fg
	}
	var out []string
	for r, cells := range grid {
		label := "   "
		if r%ch == ch/2 {
			rank := 8 - r/ch
			if b.viewer == chess.Black {
				rank = r/ch + 1
			}
			label = th.Paint(th.Dim, false, fmt.Sprintf(" %d ", rank))
		}
		// Ranks still to come in as the board opens are blank, from the far side down.
		if float64(8-r/ch) > revealed+0.5 {
			out = append(out, label+strings.Repeat(" ", 8*cw))
			continue
		}
		var line strings.Builder
		line.WriteString(label)
		for i, c := range cells {
			cursor := m.boardFocused() && th.Mono && b.cursor == m.squareAtCell(r, i, cw, ch)
			line.WriteString(paintCell(th, c, cursor))
		}
		out = append(out, line.String())
	}
	files := "   "
	for col := range 8 {
		f := col
		if b.viewer == chess.Black {
			f = 7 - col
		}
		pad := strings.Repeat(" ", cw/2)
		files += pad + string(rune('a'+f)) + strings.Repeat(" ", cw-cw/2-1)
	}
	return append(out, th.Paint(th.Dim, false, files))
}

// glitchGlyph is a piece breaking up: most frames it's a scramble character instead, always one cell wide, so
// the square around it keeps its shape.
func glitchGlyph(g string, seed int64, frame int) string {
	rng := rand.New(rand.NewSource(seed + int64(frame)*7919))
	if rng.Float64() < 0.6 {
		chars := []rune(motion.Scramble)
		return string(chars[rng.Intn(len(chars))])
	}
	return g
}

// squareAtCell is the square under a cell of the board as it's drawn.
func (m *Model) squareAtCell(row, col, cw, ch int) chess.Square {
	r, c := row/ch, col/cw
	if m.chats.board.viewer == chess.Black {
		return chess.Square{File: 7 - c, Rank: r}
	}
	return chess.Square{File: c, Rank: 7 - r}
}

// paintCell paints one cell. Without colours it keeps the piece and marks the cursor by reversing it.
func paintCell(th theme.Theme, c cell, cursor bool) string {
	if th.Mono {
		if cursor {
			return "\x1b[7m" + c.s + "\x1b[m"
		}
		return c.s
	}
	return fmt.Sprintf("\x1b[1;38;2;%d;%d;%d;48;2;%d;%d;%dm%s\x1b[m", c.fg.R, c.fg.G, c.fg.B, c.bg.R, c.bg.G, c.bg.B, c.s)
}

// lost reports whether a side lost the game: mated, or resigned.
func (m *Model) lost(g *chess.Game, c chess.Colour) bool {
	switch g.End.Why {
	case chess.EndCheckmate:
		return g.Pos.Turn() == c
	case chess.EndResign:
		return g.End.Mine == (c == g.Mine)
	}
	return false
}

// moveList is the moves in numbered pairs, the latest at the bottom, with a receipt mark on yours.
func (m *Model) moveList(rows int) []string {
	th, b := m.th, m.chats.board
	g := m.boardGame()
	if len(g.Played) == 0 {
		return []string{th.Paint(th.Dim, false, "No moves yet")}
	}
	type pair struct {
		n    int
		w, k string // white's move and black's
	}
	var pairs []pair
	for i, p := range g.Played {
		cell := m.moveCell(p)
		if i == len(g.Played)-1 && m.level.Allows(motion.Decode) && motion.Running(b.moveAt, decodeDur, m.now) {
			cell = th.Paint(th.Text, p.Mine, motion.DecodeText(p.SAN, b.moveAt.UnixNano(), motion.Progress(b.moveAt, decodeDur, m.now)))
		}
		if len(pairs) == 0 || pairs[len(pairs)-1].n != p.Ply/2 {
			pairs = append(pairs, pair{n: p.Ply / 2, w: th.Paint(th.Dim, false, "…")}) // white's is missing when a game picks up from a position
		}
		if p.Ply%2 == 0 {
			pairs[len(pairs)-1].w = cell
		} else {
			pairs[len(pairs)-1].k = cell
		}
	}
	var out []string
	for _, p := range pairs[max(len(pairs)-rows, 0):] {
		out = append(out, th.Paint(th.Dim, false, fmt.Sprintf("%3d. ", p.n+1))+fit(p.w, 10)+p.k)
	}
	return out
}

func (m *Model) moveCell(p chess.Played) string {
	th, b := m.th, m.chats.board
	cell := th.Paint(th.Text, p.Mine, p.SAN)
	if !p.Mine {
		return cell
	}
	switch b.msgs[p.Entry.Ref].State {
	case store.Delivered:
		return cell + th.Paint(th.Green, true, " ✓")
	case store.Failed:
		return cell + th.Paint(th.Red, true, " ✕")
	}
	return cell + th.Paint(th.Yellow, false, " …")
}

// statusLine says whose move it is, how the game ended, or what's wrong, in its colour.
func (m *Model) statusLine() (string, theme.RGB, []string) {
	th, b := m.th, m.chats.board
	g := m.boardGame()
	them := m.chat.Title(b.convo)
	switch {
	case b.fresh && b.game != nil && b.game.End != nil:
		return "A new game · make the first move", th.Green, nil
	case b.fresh:
		return "Make the first move to challenge " + them, th.Green, []string{th.Paint(th.Muted, false, "you're white · n then b to play black")}
	case g.Mine == chess.NoColour:
		return them + " started a game, but their first message is missing", th.Yellow, nil
	case g.End != nil:
		return m.endText(g, them), th.Text, []string{th.Paint(th.Muted, false, "any move starts a new game")}
	case g.Problem != nil:
		why := "Your boards don't match after " + g.Problem.Move.Label()
		if g.Problem.Illegal {
			why = g.Problem.Move.Label() + " can't be played on your board"
		}
		return "⚠ " + why, th.Red, []string{th.Paint(th.Muted, false, "S sends your position to put them back in step")}
	case g.Gap >= 0:
		return "Waiting for " + chess.Label(g.Gap, "…") + ", which hasn't arrived yet", th.Yellow, nil
	case g.MyTurn():
		if g.Pos.InCheck() {
			return "Your move · check!", th.Red, nil
		}
		return "Your move", th.Green, nil
	}
	return them + " to move", th.Muted, nil
}

// gameStatus is the status line, decoding in when it changes, then what's in hand: your latest move's receipt,
// a draw offer, a promotion or a move being typed.
func (m *Model) gameStatus(w int) []string {
	th, b := m.th, m.chats.board
	g := m.boardGame()
	them := m.chat.Title(b.convo)
	text, col, more := m.statusLine()
	shown := text
	if m.level.Allows(motion.Decode) && motion.Running(b.statusAt, decodeDur, m.now) {
		shown = motion.DecodeText(text, b.statusAt.UnixNano(), motion.Progress(b.statusAt, decodeDur, m.now))
	}
	out := append([]string{th.Paint(col, true, shown)}, more...)
	if g.Offer != nil && g.End == nil {
		if g.Offer.Mine {
			out = append(out, th.Paint(th.Muted, false, "You offered a draw"))
		} else {
			out = append(out, th.Paint(th.Yellow, true, them+" offers a draw · d accepts"))
		}
	}
	if r := m.receipt(); r != "" {
		out = append(out, r)
	}
	switch {
	case b.promo != nil:
		var opts []string
		for i, mv := range b.promo {
			opt := " " + glyph(chess.Piece{Kind: mv.Promo, Colour: g.Mine}) + " " + strings.ToLower(string(mv.Promo)) + " "
			if i == b.choice {
				opt = th.Block(th.Bg, th.Cyan).Render(opt)
			}
			opts = append(opts, opt)
		}
		out = append(out, th.Paint(th.Text, true, "Promote to ")+strings.Join(opts, " ")+th.Paint(th.Dim, false, "  ← → enter · esc cancel"))
	case b.typing:
		out = append(out, th.Paint(th.Magenta, true, "move ▸ ")+b.input.render(th, max(w-9, 8), false), th.Paint(th.Dim, false, "Nf3 or g1f3 · enter plays it"))
	case b.colour:
		out = append(out, th.Paint(th.Text, true, "Play which side? ")+th.Paint(th.Cyan, true, "w")+th.Paint(th.Muted, false, " white · ")+
			th.Paint(th.Cyan, true, "b")+th.Paint(th.Muted, false, " black"))
	}
	if b.note != "" {
		for _, l := range strings.Split(renderWidth.Wrap(b.note, w, " "), "\n") {
			out = append(out, th.Paint(th.Yellow, false, l))
		}
	}
	return out
}

func (m *Model) endText(g *chess.Game, them string) string {
	var how string
	won := 0 // 1 you won, -1 they did, 0 drawn
	switch g.End.Why {
	case chess.EndResign:
		how, won = them+" resigned", 1
		if g.End.Mine {
			how, won = "You resigned", -1
		}
	case chess.EndCheckmate:
		how, won = "Checkmate", 1
		if g.Pos.Turn() == g.Mine {
			won = -1
		}
	case chess.EndStalemate:
		how = "Stalemate"
	case chess.EndMaterial:
		how = "Not enough pieces left to mate"
	case chess.EndRepetition:
		how = "The same position three times"
	case chess.EndFiftyMoves:
		how = "Fifty moves without a capture or a pawn move"
	default:
		how = "Draw agreed"
	}
	switch won {
	case 1:
		return how + " · you win"
	case -1:
		return how + " · " + them + " wins"
	}
	return how + " · a draw"
}

// receipt is how your latest move is getting on: on its way, delivered, or never acknowledged.
func (m *Model) receipt() string {
	th, b := m.th, m.chats.board
	if b.fresh {
		return ""
	}
	g := m.boardGame()
	for i := len(g.Played) - 1; i >= 0; i-- {
		p := g.Played[i]
		if !p.Mine {
			continue
		}
		msg := b.msgs[p.Entry.Ref]
		them := m.chat.Title(b.convo)
		switch msg.State {
		case store.Delivered:
			return th.Paint(th.Green, false, "✓ "+them+"'s radio got "+p.Label()) + th.Paint(th.Dim, false, " · "+seconds(msg.RTT))
		case store.Failed:
			return th.Paint(th.Red, true, "✕ No receipt for "+p.Label()) + th.Paint(th.Muted, false, " · r resends")
		}
		return th.Paint(th.Yellow, false, "… Sending "+p.Label())
	}
	return ""
}

func (m *Model) boardKeys(w int) []string {
	b := m.chats.board
	if b.idle {
		return hintLines([]hint{{"c", "back to the board"}, {"↑↓", "other conversations"}, {"click", "a square to play"}}, w)
	}
	hs := []hint{{"←↑↓→", "move"}, {"enter", "pick up · put down"}, {"i", "type a move"}, {"f", "flip"}}
	if g := b.game; g == nil || g.End != nil || b.fresh {
		hs = append(hs, hint{"n", "pick a side"})
	} else {
		hs = append(hs, hint{"d", "draw"}, hint{"R", "resign"})
	}
	if m.chessLayout() == "both" {
		hs = append(hs, hint{"tab", "to the chat"}, hint{"L", "board only"})
	} else {
		hs = append(hs, hint{"L", "board and chat"})
	}
	hs = append(hs, hint{"esc", "leave it"}, hint{"c", "close"})
	return hintLines(hs, w)
}

// boardSquareAt is the square under a cell of the conversation pane, as the board was last drawn.
func (b *boardView) boardSquareAt(x, y int) (chess.Square, bool) {
	if !b.drawn || b.cw == 0 || b.ch == 0 {
		return chess.Square{}, false
	}
	row, col := y-b.at[0], x-b.at[1]
	if row < 0 || row >= 8*b.ch || col < 0 || col >= 8*b.cw {
		return chess.Square{}, false
	}
	r, c := row/b.ch, col/b.cw
	if b.viewer == chess.Black {
		return chess.Square{File: 7 - c, Rank: r}, true
	}
	return chess.Square{File: c, Rank: 7 - r}, true
}

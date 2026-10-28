package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/chess"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// boardView is a chess game open in a direct conversation, played with its contact. The game is rebuilt from
// the conversation's chess messages each time it changes; nothing is kept but where you're looking.
type boardView struct {
	convo  store.Convo
	game   *chess.Game
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
	colour bool // choosing a colour for a new game
	note   string
	// Where the board was last drawn, in the conversation pane, for clicks: its top-left square's cell.
	at     [2]int
	drawn  bool
	viewer chess.Colour // the side at the bottom when it was drawn
}

const (
	sqW         = 3  // a square is three cells: the piece and a space either side
	boardBlockW = 32 // rank labels, eight squares, and room for what's going on in the game
	chatBesideW = 30 // the least the conversation needs beside the board
)

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
	m.chats.board = &boardView{convo: cur.Convo}
	m.refreshBoard()
	b := m.chats.board
	b.cursor = chess.Square{File: 4, Rank: 1}
	if b.game != nil && b.game.Mine == chess.Black {
		b.cursor = chess.Square{File: 4, Rank: 6}
	}
}

func (m *Model) closeBoard() { m.chats.board = nil }

// refreshBoard rebuilds the game from the conversation's chess messages.
func (m *Model) refreshBoard() {
	b := m.chats.board
	if b == nil || m.chat == nil {
		return
	}
	if cur, ok := m.selectedConvo(); !ok || cur.Convo != b.convo {
		m.closeBoard() // the conversation it was in has gone from under it
		return
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
	b.game = chess.Latest(entries)
	if b.picked && (b.game == nil || !b.game.MyTurn() || len(b.game.Pos.From(b.from)) == 0) {
		b.picked, b.promo = false, nil
	}
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
	b := m.chats.board
	g := b.game
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
			m.closeBoard()
		}
	case "f":
		b.flip = !b.flip
	case "i":
		if g == nil || !g.MyTurn() {
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
		if g == nil || g.Problem == nil {
			b.note = "Your boards agree, so there's no position to send"
			return nil, true
		}
		id, fen := g.ID, g.Pos.FEN()
		m.ask("Send your position? Their board is replaced with yours", "send it", func() tea.Cmd {
			return m.sendChess(chess.Msg{Game: id, Kind: chess.KindSync, FEN: fen})
		})
	case "r":
		if msg, ok := m.failedMove(); ok {
			return m.retry(msg), true
		}
		b.note = "No move of yours needs sending again"
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

func (m *Model) notYourMove() string {
	g := m.chats.board.game
	switch {
	case g == nil:
		return "No game yet: n starts one"
	case g.End != nil:
		return "This game is over: n starts another"
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
	g := b.game
	if g == nil || !g.MyTurn() {
		b.note = m.notYourMove()
		return nil
	}
	if pc := g.Pos.At(sq); pc.Colour == g.Mine {
		if len(g.Pos.From(sq)) == 0 {
			b.picked, b.note = false, "That piece has nowhere to go"
			return nil
		}
		b.picked, b.from = true, sq
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
	mv, err := b.game.Pos.Find(b.input.text)
	if err != nil {
		b.note = strings.TrimPrefix(err.Error(), "chess: ")
		return nil
	}
	b.typing = false
	return m.playMove(mv)
}

func (m *Model) playMove(mv chess.Move) tea.Cmd {
	b := m.chats.board
	b.picked, b.promo = false, nil
	msg, err := b.game.Next(mv)
	if err != nil {
		b.note = strings.TrimPrefix(err.Error(), "chess: ")
		return nil
	}
	b.cursor = mv.To
	return m.sendChess(msg)
}

func (m *Model) drawKey() tea.Cmd {
	b := m.chats.board
	g := b.game
	switch {
	case g == nil || g.End != nil || g.Mine == chess.NoColour:
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

// boardPane is the conversation pane while chess is open: the board alone, or with the conversation beside it,
// or under it when there's no room beside.
func (m *Model) boardPane(w, rows int) []string {
	b := m.chats.board
	b.drawn = false
	if m.chessLayout() == "both" {
		switch {
		case w >= boardBlockW+3+chatBesideW:
			left := m.boardBlock(boardBlockW, rows, false)
			right := m.convoPane(w-boardBlockW-3, rows)
			if !b.chat && len(right) > 0 {
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
				out[i] = fit(l, boardBlockW) + sep + r
			}
			return out
		case rows >= 30:
			top := m.boardBlock(w, rows, false)
			out := append(top, m.th.Paint(m.th.Line, false, strings.Repeat("─", w)))
			return append(out, m.convoPane(w, rows-len(out))...)
		}
		b.chat = false
	}
	return m.boardBlock(w, rows, true)
}

// boardBlock is the board with what's going on in the game: alone, it has the move list beside it.
func (m *Model) boardBlock(w, rows int, alone bool) []string {
	th, b := m.th, m.chats.board
	g := b.game
	them := m.chat.Title(b.convo)
	title := th.Paint(th.Magenta, true, "▌") + th.Paint(th.Text, true, chess.Prefix+" CHESS · "+them)
	side := ""
	if g != nil && g.Mine != chess.NoColour {
		side = th.Paint(th.Dim, false, "  you play "+g.Mine.String())
	}
	out := []string{clip(title, w), side}
	b.viewer = chess.White
	if g != nil && g.Mine == chess.Black {
		b.viewer = chess.Black
	}
	if b.flip {
		b.viewer = b.viewer.Other()
	}
	board := m.boardLines()
	b.at, b.drawn = [2]int{len(out), 3}, true
	var moves []string
	if alone && w >= boardBlockW+16 {
		moves = m.moveList(len(board))
	}
	for i, l := range board {
		if i < len(moves) {
			l = fit(l, boardBlockW) + moves[i]
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

// boardLines draws the board, the viewer's side at the bottom, with the file letters under it.
func (m *Model) boardLines() []string {
	th, b := m.th, m.chats.board
	g := b.game
	pos := chess.Start()
	if g != nil {
		pos = g.Pos
	}
	var last chess.Played
	hasLast := false
	if g != nil {
		last, hasLast = g.Last()
	}
	targets := map[chess.Square]bool{}
	if b.picked {
		for _, mv := range pos.From(b.from) {
			targets[mv.To] = true
		}
	}
	check := pos.InCheck()
	var out []string
	for row := 0; row < 8; row++ {
		rank := 7 - row
		if b.viewer == chess.Black {
			rank = row
		}
		line := th.Paint(th.Dim, false, fmt.Sprintf(" %d ", rank+1))
		for col := 0; col < 8; col++ {
			file := col
			if b.viewer == chess.Black {
				file = 7 - col
			}
			sq := chess.Square{File: file, Rank: rank}
			pc := pos.At(sq)
			bg := th.Raised
			if (file+rank)%2 == 1 {
				bg = th.Line
			}
			if hasLast && (sq == last.From || sq == last.To) {
				bg = th.Yellow.Lerp(bg, 0.55)
			}
			if check && pc.Kind == 'K' && pc.Colour == pos.Turn() {
				bg = th.Red.Lerp(bg, 0.3)
			}
			if b.picked && sq == b.from {
				bg = th.Magenta.Lerp(bg, 0.3)
			}
			if targets[sq] && pc != (chess.Piece{}) {
				bg = th.Green.Lerp(bg, 0.45)
			}
			text, fg := "   ", th.Text
			switch {
			case pc.Colour == chess.White:
				text = " " + whiteGlyph[pc.Kind] + " "
			case pc.Colour == chess.Black:
				text, fg = " "+blackGlyph[pc.Kind]+" ", th.Magenta
			case targets[sq]:
				text, fg = " · ", th.Green
			}
			cursor := sq == b.cursor && !b.chat
			if cursor {
				bg, fg = th.Cyan, th.Bg
			}
			line += square(th, text, fg, bg, cursor)
		}
		out = append(out, line)
	}
	files := "   "
	for col := 0; col < 8; col++ {
		f := col
		if b.viewer == chess.Black {
			f = 7 - col
		}
		files += " " + string(rune('a'+f)) + " "
	}
	return append(out, th.Paint(th.Dim, false, files))
}

// square paints one square. Without colours it keeps the piece and marks the cursor by reversing it.
func square(th theme.Theme, text string, fg, bg theme.RGB, cursor bool) string {
	if th.Mono {
		if cursor {
			return "\x1b[7m" + text + "\x1b[m"
		}
		return text
	}
	return fmt.Sprintf("\x1b[1;38;2;%d;%d;%d;48;2;%d;%d;%dm%s\x1b[m", fg.R, fg.G, fg.B, bg.R, bg.G, bg.B, text)
}

// moveList is the moves in numbered pairs, the latest at the bottom, with a receipt mark on yours.
func (m *Model) moveList(rows int) []string {
	th, b := m.th, m.chats.board
	g := b.game
	if g == nil || len(g.Played) == 0 {
		return []string{th.Paint(th.Dim, false, "No moves yet")}
	}
	type pair struct {
		n    int
		w, k string // white's move and black's
	}
	var pairs []pair
	for _, p := range g.Played {
		if len(pairs) == 0 || pairs[len(pairs)-1].n != p.Ply/2 {
			pairs = append(pairs, pair{n: p.Ply / 2, w: th.Paint(th.Dim, false, "…")}) // white's is missing when a game picks up from a position
		}
		if p.Ply%2 == 0 {
			pairs[len(pairs)-1].w = m.moveCell(p)
		} else {
			pairs[len(pairs)-1].k = m.moveCell(p)
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

// gameStatus says whose move it is, how the game ended, or what's wrong, then what's in hand: your latest
// move's receipt, a draw offer, a promotion or a move being typed.
func (m *Model) gameStatus(w int) []string {
	th, b := m.th, m.chats.board
	g := b.game
	them := m.chat.Title(b.convo)
	var out []string
	switch {
	case g == nil:
		out = append(out, th.Paint(th.Text, true, "No game with "+them+" yet"))
	case g.Mine == chess.NoColour:
		out = append(out, th.Paint(th.Yellow, true, them+" started a game, but their first message is missing"))
	case g.End != nil:
		out = append(out, th.Paint(th.Text, true, m.endText(g, them)))
	case g.Problem != nil:
		why := "your boards don't match after " + g.Problem.Move.Label()
		if g.Problem.Illegal {
			why = g.Problem.Move.Label() + " can't be played on your board"
		}
		out = append(out, th.Paint(th.Red, true, "⚠ "+strings.ToUpper(why[:1])+why[1:]), th.Paint(th.Muted, false, "S sends your position to put them back in step"))
	case g.Gap >= 0:
		out = append(out, th.Paint(th.Yellow, true, "Waiting for "+chess.Label(g.Gap, "…")+", which hasn't arrived yet"))
	case g.MyTurn():
		turn := "Your move"
		if g.Pos.InCheck() {
			turn += " · check!"
		}
		out = append(out, th.Paint(th.Green, true, turn))
	default:
		out = append(out, th.Paint(th.Muted, true, them+" to move"))
	}
	if g != nil && g.Offer != nil && g.End == nil {
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
			glyph := whiteGlyph[mv.Promo]
			if b.game.Mine == chess.Black {
				glyph = blackGlyph[mv.Promo]
			}
			opt := " " + glyph + " " + strings.ToLower(string(mv.Promo)) + " "
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
	g := b.game
	if g == nil {
		return ""
	}
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
	hs := []hint{{"←↑↓→", "move"}, {"enter", "pick up · put down"}, {"i", "type a move"}, {"f", "flip"}}
	if g := b.game; g == nil || g.End != nil {
		hs = append(hs, hint{"n", "new game"})
	} else {
		hs = append(hs, hint{"d", "draw"}, hint{"R", "resign"})
	}
	if m.chessLayout() == "both" {
		hs = append(hs, hint{"tab", "to the chat"}, hint{"L", "board only"})
	} else {
		hs = append(hs, hint{"L", "board and chat"})
	}
	hs = append(hs, hint{"esc", "back"})
	return hintLines(hs, w)
}

// boardSquareAt is the square under a cell of the conversation pane, as the board was last drawn.
func (b *boardView) boardSquareAt(x, y int) (chess.Square, bool) {
	if !b.drawn {
		return chess.Square{}, false
	}
	row, col := y-b.at[0], x-b.at[1]
	if row < 0 || row >= 8 || col < 0 || col >= 8*sqW {
		return chess.Square{}, false
	}
	c := col / sqW
	sq := chess.Square{File: c, Rank: 7 - row}
	if b.viewer == chess.Black {
		sq = chess.Square{File: 7 - c, Rank: row}
	}
	return sq, true
}

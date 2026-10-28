package ui

import (
	"github.com/verdantran/meshcore-tui/internal/chess"
	"github.com/verdantran/meshcore-tui/internal/store"
)

// chessMsg reads a direct message as a chess message. Chess is only played in direct messages, so the same text
// in a channel is just text.
func chessMsg(msg store.Message) (chess.Msg, bool) {
	if msg.Convo.IsChannel() {
		return chess.Msg{}, false
	}
	return chess.Parse(msg.Text)
}

// shownText is a message as the conversation shows it: a chess message reads as what it does, as
// meshcore-companion shows it, not as its raw text.
func (m *Model) shownText(msg store.Message) string {
	cm, ok := chessMsg(msg)
	if !ok {
		return msg.Text
	}
	them := "They"
	if m.chat != nil {
		them = m.chat.Title(msg.Convo)
	}
	out := msg.Out
	what := ""
	switch cm.Kind {
	case chess.KindNew:
		what = them + " challenged you, you're " + cm.Colour.Other().String()
		if out {
			what = "New game, you're " + cm.Colour.String()
		}
	case chess.KindMove:
		what = chess.Label(cm.Ply, cm.SAN)
	case chess.KindResign:
		what = them + " resigned"
		if out {
			what = "You resigned"
		}
	case chess.KindDrawOffer:
		what = them + " offers a draw"
		if out {
			what = "You offered a draw"
		}
	case chess.KindDrawAccept:
		what = "Draw agreed"
	case chess.KindSync:
		what = them + " sent their position"
		if out {
			what = "You sent your position"
		}
	}
	return chess.Prefix + " " + what
}

// noteText is a received message as a notification or the log says it, after the sender's name: a chess
// message says what it does, as meshcore-companion's notifications do.
func noteText(msg store.Message) string {
	cm, ok := chessMsg(msg)
	if !ok {
		return msg.Text
	}
	switch cm.Kind {
	case chess.KindNew:
		return "challenged you to chess (you're " + cm.Colour.Other().String() + ")"
	case chess.KindMove:
		return "played " + chess.Label(cm.Ply, cm.SAN)
	case chess.KindResign:
		return "resigned the chess game"
	case chess.KindDrawOffer:
		return "offers a draw"
	case chess.KindDrawAccept:
		return "accepted the draw"
	}
	return "sent their chess position"
}

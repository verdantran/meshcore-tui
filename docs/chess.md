# Chess over the mesh

Chess over MeshCore comes from [meshcore-companion](https://github.com/edleeman17/meshcore-companion), the web companion by [edleeman17](https://github.com/edleeman17). The messages a game is played in, and the way a game is rebuilt from them, come from there, and meshcore-tui follows them so the two apps can play each other. That hasn't been tried yet, so for now think of it as designed to work with meshcore-companion rather than known to. Thank you, edleeman17.

## Playing

Open a direct conversation with someone and press `c`. Chess is only for people: not channels, rooms or repeaters.

With no game yet, the board is set up with you as white: make the first move and it goes to them with the challenge. To play black instead, press `n` then `b`, and wait for their move. When a game's over, any move starts the next one. When it's your move:

- **Cursor:** the arrow keys (or `h j k l`) move round the board. `enter` or `space` picks up one of your pieces and marks where it can go, and again puts it down. `esc` puts it back.
- **Mouse:** click a piece, then where it goes.
- **Typing:** `i`, then the move: `Nf3`, `exd5`, `O-O`, or the squares, like `g1f3`. A promotion names its piece: `e8=Q`, or `e7e8q`.

When a pawn reaches the far side by cursor or mouse, you pick what it becomes: `←` `→` and `enter`, or `q` `r` `b` `n`.

| Key | |
|---|---|
| `f` | turn the board round |
| `d` | offer a draw, or accept the one they offered; making a move turns an offer down |
| `R` | resign, once you've confirmed it |
| `S` | send your whole position: when your boards don't match, or their side lost the game |
| `r` | send your latest move again: if it never got a receipt, or their side lost it anyway |
| `n` | pick a side for a new game: `w` or `b` |
| `L` | the board alone, or with the conversation beside it |
| `tab` | between the board and the conversation beside it |
| `esc` | leave the board open and go back to the list, to pick another conversation; `c` or a click comes back to it |
| `c` | close the board |

`?` lists these while the board is open.

The board is as big as the space allows, with the pieces drawn as pixel art, lit from above, in ivory and ink. On the smallest board, and in ASCII mode, they're single symbols or letters instead. Moves glide across it, flashing the squares as they land; captures and a lost king break up. They follow the app's motion setting, so reduced keeps the flashes and off keeps it still.

The board sits beside the conversation, so you can chat as you play; `L` shows it alone instead, and the app remembers which you chose (`chess_layout` in the [config](configuration.md)). In a window too narrow for both, the conversation goes under the board, or, if there's no room for that either, the board shows alone.

A game is kept in the conversation's messages, not anywhere else, so it carries on after the app is closed, from where both of you left it.

In `--demo`, the made-up people play back: challenge one and they answer each move after a moment's thought. The demo starts with a game under way, too: Dan has challenged you and opened.

## How it works

Each move is one short direct message, so it travels like any other and gets the usual delivery receipt. The board shows whether your latest move has arrived: `…` while it's on its way, `✓` once their radio has it, `✕` if no receipt came, and `r` sends it again.

The messages are plain text, so any MeshCore app shows them; meshcore-tui shows them as what they do, like "♟ 12. Nf3":

```
♟g3k new white        a challenge: the sender plays white (or black)
♟g3k 12. Nf3 #a41c    white's 12th move, and a checksum of the position after it
♟g3k 12... Nf6 #0b3e  black's
♟g3k resign
♟g3k draw?            a draw offered
♟g3k draw ok          and accepted
♟g3k sync <FEN>       the sender's whole position
```

`g3k` names the game, so a new game in the same conversation starts afresh. The board always shows the latest one.

A game is rebuilt from the conversation's chess messages every time one arrives, in the order you sent and received them. A move counts once however many copies arrive, and for your own moves, a copy that got through counts over one that didn't.

The checksum is the first four hex digits of the FNV-1a hash of the position's board, side to move and castling rights. If a move goes missing, or turns up twice, the position after it won't match its checksum, and the board says so. `S` then sends your whole position in a `sync` message, which replaces everything before it on their board too, so both match again.

A position in a `sync` comes from whoever sent it, so it's checked before it's played on: one king each, no pawns on the first or last rank, the side that just moved not left in check, and castling and en passant only where the pieces allow them. One that fails is ignored.

The rules are [corentings/chess](https://github.com/corentings/chess) (MIT), built on Logan Spears' original.

package sim

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/verdantran/meshcore-tui/internal/chess"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// chessFrom waits for the next chess message from a person, setting aside everything else the demo says.
func chessFrom(t *testing.T, ctx context.Context, s *Sim, name string) chess.Msg {
	t.Helper()
	from := prefix(key(peopleHash(name), name))
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("no chess message from %s", name)
		case e := <-s.Events():
			if in, ok := e.(radio.Incoming); ok && in.From == from {
				if msg, ok := chess.Parse(in.Text); ok {
					return msg
				}
			}
		}
	}
}

func TestTheDemoStartsWithAGameAndPlaysBack(t *testing.T) {
	s := New(3).Fast().Lossless()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.Start(ctx)
	defer s.Close()

	// Dan has challenged you and opened.
	if m := chessFrom(t, ctx, s, "Dan"); m.Kind != chess.KindNew || m.Colour != chess.White {
		t.Fatalf("Dan's challenge: %+v", m)
	}
	if m := chessFrom(t, ctx, s, "Dan"); m.Kind != chess.KindMove || m.Ply != 0 {
		t.Fatalf("Dan's first move: %+v", m)
	}

	// Challenge Alice as black: she's white, so she moves first, with the checksum of the position after it.
	alice := key(peopleHash("Alice"), "Alice")
	if _, err := s.SendText(ctx, alice, "♟abc new black"); err != nil {
		t.Fatal(err)
	}
	m := chessFrom(t, ctx, s, "Alice")
	if m.Kind != chess.KindMove || m.Ply != 0 || m.Game != "abc" {
		t.Fatalf("Alice's opening: %+v", m)
	}
	mv, err := chess.Start().Find(m.SAN)
	if err != nil {
		t.Fatalf("Alice's %s isn't legal: %v", m.SAN, err)
	}
	after, _ := chess.Start().Play(mv)
	if chess.Checksum(after.FEN()) != m.Check {
		t.Errorf("Alice's checksum %s doesn't match the position", m.Check)
	}

	// A game you resign gets no more moves from her.
	if _, err := s.SendText(ctx, alice, "♟abc resign"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	s.mu.Lock()
	log := s.chessLog["Alice"]
	s.mu.Unlock()
	if last := log[len(log)-1]; !strings.HasSuffix(last.Text, "resign") {
		t.Errorf("Alice answered a resignation: %q", last.Text)
	}
}

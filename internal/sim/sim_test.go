package sim

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
)

func TestBootThenPacketsThatDecode(t *testing.T) {
	s := New(1).Fast()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Start(ctx)
	defer s.Close()

	var ready bool
	packets := 0
	for packets < 20 {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out: ready=%v packets=%d", ready, packets)
		case e := <-s.Events():
			switch e := e.(type) {
			case radio.Ready:
				ready = true
				if e.Self.Params() != "869.618 SF8 BW62.5" {
					t.Errorf("params = %q", e.Self.Params())
				}
			case radio.Packet:
				if !ready {
					t.Fatal("packet before Ready")
				}
				p, err := meshcore.PacketFromBytes(e.Raw)
				if err != nil {
					t.Fatalf("simulated packet doesn't decode: %v (% X)", err, e.Raw)
				}
				if p.PayloadTypeString() == "" || p.RouteTypeString() == "" {
					t.Fatalf("unnamed packet type: % X", e.Raw)
				}
				packets++
			}
		}
	}
}

func TestBootDetailsMatchReady(t *testing.T) {
	s := New(2).Fast()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Start(ctx)
	defer s.Close()
	details := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out before Ready")
		case e := <-s.Events():
			switch e := e.(type) {
			case radio.BootStep:
				if e.State == radio.StepOK {
					details[e.Key] = e.Detail
				}
			case radio.Ready:
				if want := fmt.Sprintf("%d synced", e.Contacts); details["contacts"] != want {
					t.Errorf("contacts step said %q, Ready has %d", details["contacts"], e.Contacts)
				}
				if want := fmt.Sprintf("%d configured", e.Channels); details["channels"] != want {
					t.Errorf("channels step said %q, Ready has %d", details["channels"], e.Channels)
				}
				if details["hello"] != e.Self.Name {
					t.Errorf("hello step said %q, Ready has %q", details["hello"], e.Self.Name)
				}
				if len(e.ContactList) != e.Contacts || len(e.ChannelList) != e.Channels {
					t.Errorf("Ready lists %d contacts and %d channels but counts %d and %d", len(e.ContactList), len(e.ChannelList), e.Contacts, e.Channels)
				}
				return
			}
		}
	}
}

func waitFor[T any](t *testing.T, s *Sim) T {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case <-timeout:
			var zero T
			t.Fatalf("timed out waiting for %T", zero)
		case e := <-s.Events():
			if v, ok := e.(T); ok {
				return v
			}
		}
	}
}

func TestContactOperations(t *testing.T) {
	s := New(3).Fast()
	ctx := context.Background()
	book, _ := s.SyncContacts(ctx)
	n := len(book)

	tag, err := s.Trace(ctx, []byte{0xA3, 0x1E, 0xA3})
	if err != nil {
		t.Fatal(err)
	}
	tr := waitFor[radio.TraceResult](t, s)
	if tr.Tag != tag || len(tr.SNRs) != 3 {
		t.Fatalf("trace result = %+v", tr)
	}

	alice := key(0x3A, "Alice")
	if err := s.DiscoverPath(ctx, alice); err != nil {
		t.Fatal(err)
	}
	pf := waitFor[radio.PathFound](t, s)
	if pf.Prefix != prefix(alice) || len(pf.Out) == 0 || len(pf.In) != len(pf.Out) {
		t.Fatalf("path found = %+v", pf)
	}
	book, _ = s.SyncContacts(ctx)
	for _, c := range book {
		if c.Key == alice && c.PathLen != len(pf.Out) {
			t.Fatalf("discovery should store the route; alice = %+v", c)
		}
	}

	link, err := s.ExportContact(ctx, alice)
	if err != nil || !strings.HasPrefix(link, radio.LinkPrefix) {
		t.Fatalf("export = %q, %v", link, err)
	}
	if err := s.RemoveContact(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if book, _ = s.SyncContacts(ctx); len(book) != n-1 {
		t.Fatalf("after remove: %d contacts", len(book))
	}
	if err := s.ImportContact(ctx, link); err != nil {
		t.Fatal(err)
	}
	if book, _ = s.SyncContacts(ctx); len(book) != n {
		t.Fatalf("after import: %d contacts", len(book))
	}
	if err := s.ImportContact(ctx, "meshcore://zz"); err == nil {
		t.Fatal("a damaged link should be refused")
	}

	s.SetAutoAdd(ctx, radio.AutoAdd{Chat: true})
	if a, _ := s.AutoAdd(ctx); !a.Chat || a.Repeater {
		t.Fatalf("auto-add = %+v", a)
	}
}

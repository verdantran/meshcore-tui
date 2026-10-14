package sim

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/packets"
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

// TestPacketsAreReal checks the simulator's traffic decodes like a real radio's: signed adverts,
// channel messages encrypted with the channel secrets it reports.
func TestPacketsAreReal(t *testing.T) {
	s := New(9).Fast()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Start(ctx)
	defer s.Close()
	var book packets.Book
	adverts, decrypted := 0, 0
	for n := 0; n < 300; {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out after %d packets (%d adverts, %d decrypted)", n, adverts, decrypted)
		case e := <-s.Events():
			switch e := e.(type) {
			case radio.Ready:
				book = packets.Book{Self: e.Self.PubKey, Channels: e.ChannelList, Contacts: e.ContactList}
			case radio.Packet:
				n++
				d := packets.Decode(e.Raw, book)
				if d.Err != nil {
					t.Fatalf("simulated packet doesn't decode: %v", d.Err)
				}
				if d.Type == "ADVERT" {
					adverts++
					if !d.SigOK {
						t.Fatalf("advert from %s has a bad signature", d.Name)
					}
				}
				if d.Type == "GRP_TXT" {
					if !d.Decrypted {
						t.Fatalf("channel packet didn't decrypt: %s", d.Summary)
					}
					decrypted++
				}
			}
		}
	}
	if adverts == 0 || decrypted == 0 {
		t.Fatalf("expected adverts and channel messages, got %d and %d", adverts, decrypted)
	}
}

func TestMessagingCalls(t *testing.T) {
	s := New(4).Fast().Lossless()
	ctx := context.Background()
	if s.Transport() != "SIM" || s.Describe() != "simulated radio" {
		t.Error("describe")
	}
	alice := key(0x3A, "Alice")
	sent, err := s.SendText(ctx, alice, "hello")
	if err != nil || sent.Ack == 0 || sent.Timeout == 0 {
		t.Fatalf("SendText = %+v, %v", sent, err)
	}
	if a := waitFor[radio.Acked](t, s); a.Ack != sent.Ack {
		t.Errorf("ack %d for send %d", a.Ack, sent.Ack)
	}
	if _, err := s.SendText(ctx, key(0xA3, "Dundry"), "hi"); err == nil {
		t.Error("repeaters can't be messaged")
	}
	if _, err := s.SendText(ctx, alice, strings.Repeat("x", radio.MaxText+1)); err == nil {
		t.Error("too long")
	}
	if err := s.SendChannel(ctx, 9, "x"); err == nil {
		t.Error("no such channel")
	}
	if err := s.SendChannel(ctx, 1, "anyone about?"); err != nil {
		t.Fatal(err)
	}
	if in := waitFor[radio.Incoming](t, s); !in.Channel || !strings.Contains(in.Text, "loud and clear") {
		t.Errorf("reply = %+v", in)
	}
	if s.ResetPath(ctx, alice) != nil {
		t.Error("reset path")
	}
	if ap, err := s.AdvertPath(ctx, alice); err != nil || ap.At.IsZero() {
		t.Errorf("advert path = %+v, %v", ap, err)
	}
	if _, err := s.AdvertPath(ctx, [32]byte{}); err == nil {
		t.Error("advert path for a stranger")
	}
	if peopleHash("nobody") != 0 {
		t.Error("people hash")
	}
	if s.reroute([]byte{1}) != nil {
		t.Error("reroute of junk")
	}
	if _, err := s.Trace(ctx, []byte{0xEE}); err != nil {
		t.Error("a trace through an unknown hash is sent, just never answered")
	}
	for _, f := range []func() error{
		func() error { return s.DiscoverPath(ctx, [32]byte{}) },
		func() error { return s.RemoveContact(ctx, [32]byte{}) },
		func() error { _, err := s.ExportContact(ctx, [32]byte{}); return err },
	} {
		if f() == nil {
			t.Error("operations on a non-contact should fail")
		}
	}
	if err := s.ImportContact(ctx, radio.LinkPrefix+strings.Repeat("ab", 40)); err == nil {
		t.Error("links the simulator didn't make are refused")
	}
}

func TestStrangersAndAutoAdd(t *testing.T) {
	s := New(6).Fast()
	now := time.Now()
	for i := 0; i < 40; i++ {
		s.stranger(now)
	}
	heard := false
	for done := false; !done; {
		select {
		case e := <-s.Events():
			_, heard = e.(radio.Heard)
			done = heard
		default:
			done = true
		}
	}
	if !heard {
		t.Fatal("strangers should be heard while auto-add is off for chat nodes")
	}
	s.SetAutoAdd(context.Background(), radio.AutoAdd{Chat: true, Repeater: true, Room: true, Sensor: true})
	before := len(s.SnapshotContacts())
	for i := 0; i < 40; i++ {
		s.stranger(now)
	}
	if len(s.SnapshotContacts()) <= before {
		t.Error("with auto-add on, strangers become contacts")
	}
}

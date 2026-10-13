package sim

import (
	"context"
	"fmt"
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

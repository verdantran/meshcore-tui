package sim

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
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

	tag, err := s.Trace(ctx, []byte{0xA3, 0x1E, 0xA3}, 1)
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
	if _, err := s.Trace(ctx, []byte{0xEE}, 1); err != nil {
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

func TestAdminServers(t *testing.T) {
	s := New(8).Fast()
	ctx := context.Background()
	dundry, room := key(0xA3, "Dundry"), key(0x6C, "Clifton Room")

	// Nothing answers before login.
	s.RequestStatus(ctx, dundry)
	s.SendCLI(ctx, dundry, "ver")
	if err := s.Login(ctx, [32]byte{}, "x"); err == nil {
		t.Error("login to a non-server")
	}
	s.Login(ctx, dundry, "wrong")
	if r := waitFor[radio.LoginResult](t, s); r.OK {
		t.Fatal("wrong password accepted")
	}
	s.Login(ctx, dundry, "")
	if r := waitFor[radio.LoginResult](t, s); !r.OK || r.Admin {
		t.Fatalf("guest login = %+v", r)
	}
	s.SendCLI(ctx, dundry, "ver") // guests get no console
	s.Login(ctx, dundry, adminPassword)
	if r := waitFor[radio.LoginResult](t, s); !r.OK || !r.Admin || r.Permissions != 3 {
		t.Fatalf("admin login = %+v", r)
	}

	s.RequestStatus(ctx, dundry)
	st := waitFor[radio.StatusReport](t, s)
	if stats, err := meshcore.ParseRepeaterStats(st.Data); err != nil || stats.UpTimeSecs < 1051200 || stats.RecvErrors != 14 {
		t.Errorf("status = %+v, %v", stats, err)
	}
	s.RequestTelemetry(ctx, dundry)
	if tel := waitFor[radio.Telemetry](t, s); len(tel.LPP) == 0 {
		t.Error("telemetry empty")
	}
	tag, _ := s.BinaryRequest(ctx, dundry, meshcore.BuildNeighboursRequest(meshcore.NeighboursRequest{Count: 10, PrefixLen: 6}, [4]byte{}))
	br := waitFor[radio.BinaryReply](t, s)
	if nb, err := meshcore.ParseNeighboursReply(br.Data, 6); br.Tag != tag || err != nil || len(nb.Neighbours) == 0 {
		t.Errorf("neighbours = %+v, %v", nb, err)
	}
	s.BinaryRequest(ctx, dundry, meshcore.BuildRequest(meshcore.ReqTypeGetAccessList, [4]byte{}))
	if acl, err := meshcore.ParseAccessListReply(waitFor[radio.BinaryReply](t, s).Data); err != nil || len(acl) != 2 {
		t.Errorf("acl = %+v, %v", acl, err)
	}
	s.BinaryRequest(ctx, dundry, meshcore.BuildRequest(meshcore.ReqTypeGetOwnerInfo, [4]byte{}))
	if oi, _ := meshcore.ParseOwnerInfoReply(waitFor[radio.BinaryReply](t, s).Data); oi.Name != "Dundry" {
		t.Errorf("owner info = %+v", oi)
	}

	bob := key(0xB0, "Bob")
	bobHex := radio.KeyHex(bob[:])
	console := func(cmd string) string {
		t.Helper()
		s.SendCLI(ctx, dundry, cmd)
		in := waitFor[radio.Incoming](t, s)
		if !in.CLI {
			t.Fatalf("%s: reply isn't marked as console output", cmd)
		}
		return in.Text
	}
	for _, c := range [][2]string{
		{"get tx", "> 22"}, {"set tx 30", "Error: Transmit power must be between 1 and 22 dBm"}, {"set tx 20", "OK"},
		{"set radio 869.525,250,11,5", "OK - reboot to apply"}, {"get nope", "Unknown config: nope"}, {"set nope 1", "Unknown config: nope"},
		{"ver", "v1.17.1 (Build: 14 Aug 2026)"}, {"board", "Heltec V4"}, {"advert", "OK - Advert sent"}, {"advert.zerohop", "OK - zerohop advert sent"},
		{"log start", "OK - logging started"}, {"log stop", "OK - logging stopped"}, {"log erase", "OK - log erased"},
		{"clear stats", "OK - stats reset"}, {"password hunter2", "password now: hunter2"}, {"setperm 3a 2", "Err - invalid params"}, {"setperm " + bobHex + " 2", "OK"},
		{"setperm b0 0", "OK"}, {"setperm ee 0", "Err - invalid params"}, {"setperm b0", "Err - bad params"},
		{"region", "* F\n bristol^ F\n  bath F\n wales\n"}, {"region home wales", " home is now wales"},
		{"region denyf bath", "OK"}, {"region allowf nowhere", "Err - unknown region"}, {"region put cardiff wales", "OK - (flood allowed)"},
		{"region put x nowhere", "Err - unknown parent"}, {"region remove wales", "Err - not empty"}, {"region remove cardiff", "OK"},
		{"region remove nowhere", "Err - not found"}, {"region put somerset", "OK - (flood allowed)"}, {"region save", "OK"},
		{"region frob", "Err - ??"}, {"region frob bath", "Err - ??"},
		{"region", "* F\n bristol F\n  bath\n wales^\n somerset F\n"}, {"dance", "Unknown command"},
	} {
		if got := console(c[0]); got != c[1] {
			t.Errorf("%s → %q, want %q", c[0], got, c[1])
		}
	}
	if got := console("get tx"); got != "> 20" {
		t.Errorf("set didn't stick: %q", got)
	}
	for _, cmd := range []string{"clock", "clock sync"} {
		if console(cmd) == "" {
			t.Errorf("%s gave nothing", cmd)
		}
	}
	s.SendCLI(ctx, dundry, "reboot") // no reply, by design
	s.Logout(ctx, dundry)

	// The room server sends its posts after login and keeps what you post.
	s.Login(ctx, room, roomPassword)
	waitFor[radio.LoginResult](t, s)
	first := waitFor[radio.Incoming](t, s)
	if !first.Signed || first.Author == [4]byte{} || !strings.Contains(first.Text, "Welcome") {
		t.Errorf("first post = %+v", first)
	}
	s.SendText(ctx, room, "range test at 7?")
	if n := len(s.posts); n != len(seededPosts(time.Now()))+1 {
		t.Errorf("posts = %d", n)
	}
	s.RequestStatus(ctx, room)
	if _, err := meshcore.ParseRoomStats(waitFor[radio.StatusReport](t, s).Data); err != nil {
		t.Error(err)
	}
}

func TestSimSigns(t *testing.T) {
	s := New(1).Fast()
	ctx := context.Background()
	data := signing.Canonical("#bristol", 1760000000, "hello")
	sig, err := s.Sign(ctx, data)
	self := key(0xC0, "MeshCore-k4")
	if err != nil || !ed25519.Verify(self[:], data, sig[:]) {
		t.Fatal("the radio signs with its own key")
	}
	if ts, err := s.SendChannelAt(ctx, 1, "hi"); err != nil || ts == 0 {
		t.Errorf("send at = %d %v", ts, err)
	}
	if err := s.SendChannelData(ctx, 1, signing.DataType, []byte("MC1")); err != nil {
		t.Error(err)
	}
	if err := s.SendChannelData(ctx, 9, signing.DataType, nil); err == nil {
		t.Error("no such channel")
	}
	alice := key(peopleHash("Alice"), "Alice")
	d, ok := signing.Parse(signedDatagram(ident(peopleHash("Alice"), "Alice"), "#bristol", 1760000000, "meet"))
	if !ok || !d.Verify(alice, "#bristol", "meet") {
		t.Error("Alice's signatures check out against her key")
	}
}

// With the device set to 2-byte path hashes, the paths it makes up tell Kelston and Lansdown apart.
func TestTwoBytePaths(t *testing.T) {
	s := New(9).Fast()
	ctx := context.Background()
	if err := s.SetPathHashMode(ctx, 1); err != nil {
		t.Fatal(err)
	}
	book := packets.Book{Self: device.self.PubKey, Contacts: contacts()}
	for range 200 {
		raw, _, _ := s.packet(time.Now())
		if raw == nil {
			continue
		}
		d := packets.Decode(raw, book)
		if d.Err != nil || d.HashSize != 2 {
			t.Fatalf("packet = %s, %d-byte hops", d.Summary, d.HashSize)
		}
		for _, h := range d.Hops {
			if h.Key == ([32]byte{}) || h.Guess {
				t.Fatalf("hop %X isn't named surely: %+v", h.Hash, h)
			}
		}
	}
	kelston := key(0x5F, "Kelston")
	tag, err := s.Trace(ctx, kelston[:2], 2)
	if err != nil {
		t.Fatal(err)
	}
	if tr := waitFor[radio.TraceResult](t, s); tr.Tag != tag || tr.HashSize != 2 || len(tr.SNRs) != 1 {
		t.Fatalf("trace result = %+v", tr)
	}
	if _, err := s.Trace(ctx, kelston[:3], 3); err == nil {
		t.Error("a trace can't carry 3-byte hops")
	}
	if err := s.DiscoverPath(ctx, key(0x3A, "Alice")); err != nil {
		t.Fatal(err)
	}
	if pf := waitFor[radio.PathFound](t, s); pf.OutSize != 2 || len(pf.Out)%2 != 0 || len(pf.Out) == 0 {
		t.Fatalf("path found = %+v", pf)
	}
}

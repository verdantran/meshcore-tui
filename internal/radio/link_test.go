package radio

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OwlShack/meshcore-go/companion"
)

func startLink(t *testing.T, f *fakeDevice) *Link {
	t.Helper()
	l := newLink("/dev/cu.fake", "USB", f)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); l.Close() })
	l.Start(ctx)
	return l
}

// next waits for an event of type T, collecting the ones it passes over.
func next[T any](t *testing.T, l *Link, skipped *[]Event) T {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case <-timeout:
			var zero T
			t.Fatalf("timed out waiting for %T", zero)
		case e := <-l.Events():
			if v, ok := e.(T); ok {
				return v
			}
			if skipped != nil {
				*skipped = append(*skipped, e)
			}
		}
	}
}

func TestConnectSequence(t *testing.T) {
	f := newFakeDevice()
	l := startLink(t, f)
	if l.Describe() != "/dev/cu.fake" || l.Transport() != "USB" {
		t.Fatal("describe/transport")
	}
	var before []Event
	ready := next[Ready](t, l, &before)

	steps := map[string]BootStep{}
	frames, transmits := 0, 0
	for _, e := range before {
		switch e := e.(type) {
		case BootStep:
			if e.State != StepWait {
				steps[e.Key] = e
			}
		case Frame:
			frames++
		case Transmit:
			transmits++
		}
	}
	for _, key := range []string{"link", "hello", "device", "clock", "contacts", "channels", "queue", "power"} {
		if steps[key].State != StepOK {
			t.Errorf("step %s = %+v", key, steps[key])
		}
	}
	if steps["clock"].Detail == "in sync" || !f.clockSet {
		t.Errorf("a 30 s drift should be synced: %q", steps["clock"].Detail)
	}
	if steps["queue"].Detail != "2 received" || steps["channels"].Detail != "2 configured" {
		t.Errorf("queue %q channels %q", steps["queue"].Detail, steps["channels"].Detail)
	}
	if frames == 0 || transmits != 0 {
		t.Errorf("frames %d, transmits %d (nothing in the sequence goes over the air)", frames, transmits)
	}

	if ready.Self.Name != "MeshCore-k4" || ready.Self.Params() != "869.618 SF8 BW62.5" || ready.Self.Lat != 51.44 {
		t.Errorf("self = %+v", ready.Self)
	}
	if ready.Device.Model != "Heltec V4" || ready.Device.Firmware != "v1.17.1" || ready.Device.MaxChannels != 4 {
		t.Errorf("device = %+v", ready.Device)
	}
	if ready.Battery.MilliVolts != 4020 || ready.Contacts != 2 || ready.Channels != 2 {
		t.Errorf("counts = %+v", ready)
	}
	alice, dundry := ready.ContactList[0], ready.ContactList[1]
	if alice.PathLen != 2 || alice.Path[1] != 0x1E || alice.Type != TypeChat || alice.Lat != 51.346 {
		t.Errorf("alice = %+v", alice)
	}
	if dundry.PathLen != -1 || dundry.Path != nil {
		t.Errorf("a 0xFF route means flood: %+v", dundry)
	}
	if ready.ChannelList[1].Name != "#bristol" || ready.ChannelList[1].Secret[0] != 9 {
		t.Errorf("channels = %+v", ready.ChannelList)
	}

	// Messages queued while away arrive after Ready, so the UI can store them.
	dm := next[Incoming](t, l, nil)
	ch := next[Incoming](t, l, nil)
	if dm.Channel || dm.Hops != -1 || dm.From != [6]byte{0x3A, 0xAA} || dm.Text != "ping me" || !dm.HasSNR || dm.SNR != 9.5 {
		t.Errorf("dm = %+v", dm)
	}
	if !ch.Channel || ch.ChannelIdx != 1 || ch.Hops != 3 || ch.Text != "Carol: morning" || ch.SNR != -1.25 {
		t.Errorf("channel = %+v", ch)
	}
}

func TestPushes(t *testing.T) {
	f := newFakeDevice()
	f.queue = nil
	l := startLink(t, f)
	next[Ready](t, l, nil)

	f.push(companion.PushLogRxDataResponse{LastSNR: 7.25, LastRSSI: -92, Raw: []byte{0x15, 0x01, 0xA3}}.ToBytes())
	if p := next[Packet](t, l, nil); p.SNR != 7.25 || p.RSSI != -92 || len(p.Raw) != 3 {
		t.Errorf("packet = %+v", p)
	}

	f.push(companion.PushNewAdvertResponse{PublicKey: [32]byte{0xE5}, Type: 1, OutPathLen: 0xFF, AdvertName: "Eve"}.ToBytes())
	if h := next[Heard](t, l, nil); h.Contact.Name != "Eve" || h.Contact.PathLen != -1 {
		t.Errorf("NEW_ADVERT is a node that isn't a contact: %+v", h)
	}

	f.push(companion.PushAdvertResponse{PublicKey: [32]byte{0x3A, 0xAA}}.ToBytes())
	if c := next[ContactUpdated](t, l, nil); c.Contact.Name != "Alice" {
		t.Errorf("ADVERT should re-fetch the contact: %+v", c)
	}
	f.push(companion.PushPathUpdatedResponse{PublicKey: [32]byte{0xA3, 0x01}}.ToBytes())
	if c := next[ContactUpdated](t, l, nil); c.Contact.Name != "Dundry" {
		t.Errorf("PATH_UPDATED should re-fetch the contact: %+v", c)
	}

	f.push(companion.PushSendConfirmedResponse{AckCode: 0xA0C1, RoundTrip: 1450}.ToBytes())
	if a := next[Acked](t, l, nil); a.Ack != 0xA0C1 || a.RTT != 1450*time.Millisecond {
		t.Errorf("acked = %+v", a)
	}

	f.mu.Lock()
	f.queue = [][]byte{companion.ContactMsgRecvResponse{PubKeyPrefix: [6]byte{0x3A, 0xAA}, PathLen: 2, Text: "are you there"}.ToBytes()}
	f.mu.Unlock()
	f.push(companion.PushMsgWaitingResponse{}.ToBytes())
	if in := next[Incoming](t, l, nil); in.Text != "are you there" || in.Hops != 2 || in.HasSNR {
		t.Errorf("MSG_WAITING should drain the queue: %+v", in)
	}

	f.push(companion.PushContactDeletedResponse{PublicKey: [32]byte{0xA3, 0x01}}.ToBytes())
	if r := next[ContactRemoved](t, l, nil); r.Key[0] != 0xA3 {
		t.Errorf("removed = %+v", r)
	}
	f.push(companion.PushContactsFullResponse{}.ToBytes())
	next[ContactsFull](t, l, nil)

	f.push(companion.PushTraceDataResponse{PathLen: 2, Tag: 77, PathHashes: []byte{0xA3, 0x1E}, PathSnrs: []byte{28, 0xF8}, LastSNR: 6}.ToBytes())
	if tr := next[TraceResult](t, l, nil); tr.Tag != 77 || len(tr.SNRs) != 2 || tr.SNRs[0] != 7 || tr.SNRs[1] != -2 || tr.LastSNR != 6 {
		t.Errorf("trace = %+v", tr)
	}
	f.push(companion.PushPathDiscoveryResp{PubKeyPrefix: [6]byte{0x3A}, OutPathLen: 2, OutPath: []byte{0xA3, 0x1E}, InPathLen: 1, InPath: []byte{0x1E}}.ToBytes())
	if pf := next[PathFound](t, l, nil); len(pf.Out) != 2 || len(pf.In) != 1 {
		t.Errorf("path found = %+v", pf)
	}
}

func TestCommands(t *testing.T) {
	f := newFakeDevice()
	f.queue = nil
	l := startLink(t, f)
	next[Ready](t, l, nil)
	ctx := context.Background()

	sent, err := l.SendText(ctx, [32]byte{0x3A, 0xAA}, "hello")
	if err != nil || sent.Ack != 0xA0C1 || sent.Timeout != 4200*time.Millisecond || sent.Flood {
		t.Fatalf("SendText = %+v, %v (the ack comes from the extended SENT frame)", sent, err)
	}
	var sawTransmit bool
	for !sawTransmit {
		_, sawTransmit = next[Transmit](t, l, nil), true
	}
	if err := l.SendChannel(ctx, 1, "hi all"); err != nil {
		t.Fatal(err)
	}
	if err := l.ResetPath(ctx, [32]byte{0x3A}); err != nil {
		t.Fatal(err)
	}

	cs, err := l.SyncContacts(ctx)
	if err != nil || len(cs) != 2 {
		t.Fatalf("SyncContacts = %d, %v", len(cs), err)
	}
	save := cs[0]
	save.Flags, save.Path, save.PathLen = FlagFavourite, []byte{0x90, 0xB2, 0xA3}, 3
	if err := l.SaveContact(ctx, save); err != nil {
		t.Fatal(err)
	}
	flood := cs[1]
	if err := l.SaveContact(ctx, flood); err != nil {
		t.Fatal(err)
	}
	var updates []companion.AddUpdateContactCommand
	for _, c := range f.commands() {
		if u, ok := c.(companion.AddUpdateContactCommand); ok {
			updates = append(updates, u)
		}
	}
	if len(updates) != 2 || updates[0].OutPathLen != 3 || updates[0].OutPath[2] != 0xA3 || updates[0].Flags != FlagFavourite ||
		updates[0].Name != "Alice" || updates[0].Latitude != 51346000 || updates[1].OutPathLen != 0xFF {
		t.Fatalf("contact updates = %+v", updates)
	}
	if err := l.RemoveContact(ctx, [32]byte{0xA3, 0x01}); err != nil {
		t.Fatal(err)
	}

	tag, err := l.Trace(ctx, []byte{0xA3, 0x1E, 0xA3})
	if err != nil {
		t.Fatal(err)
	}
	var trace companion.SendTracePathCommand
	for _, c := range f.commands() {
		if tc, ok := c.(companion.SendTracePathCommand); ok {
			trace = tc
		}
	}
	if trace.Tag != tag || string(trace.Path) != string([]byte{0xA3, 0x1E, 0xA3}) || trace.Flags != 0 {
		t.Fatalf("trace command = %+v (tag %d)", trace, tag)
	}
	if err := l.DiscoverPath(ctx, [32]byte{0x3A}); err != nil {
		t.Fatal(err)
	}
	ap, err := l.AdvertPath(ctx, [32]byte{0x3A})
	if err != nil || len(ap.Path) != 2 || ap.At.Unix() != 1760000400 {
		t.Fatalf("AdvertPath = %+v, %v", ap, err)
	}

	link, err := l.ExportContact(ctx, [32]byte{0x3A})
	if err != nil || !strings.HasPrefix(link, LinkPrefix+"11") {
		t.Fatalf("ExportContact = %q, %v", link, err)
	}
	if err := l.ImportContact(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := l.ImportContact(ctx, "https://example.com"); err == nil {
		t.Fatal("a non-meshcore link should be refused before reaching the radio")
	}

	a, err := l.AutoAdd(ctx)
	if err != nil || !a.Repeater || a.Chat || !a.OverwriteOldest || a.MaxHops != 3 {
		t.Fatalf("AutoAdd = %+v, %v", a, err)
	}
	a.Chat, a.Repeater, a.Room, a.Sensor, a.MaxHops = true, false, true, true, 99
	if err := l.SetAutoAdd(ctx, a); err != nil {
		t.Fatal(err)
	}
	back, _ := l.AutoAdd(ctx)
	if !back.Chat || back.Repeater || !back.Room || !back.Sensor || back.MaxHops != 64 {
		t.Fatalf("after SetAutoAdd = %+v (hops are capped at 64)", back)
	}
}

func TestRadioErrors(t *testing.T) {
	f := newFakeDevice()
	l := startLink(t, f)
	next[Ready](t, l, nil)
	ctx := context.Background()
	for code, call := range map[byte]func() error{
		companion.CmdSendTxtMsg:           func() error { _, err := l.SendText(ctx, [32]byte{1}, "x"); return err },
		companion.CmdGetContacts:          func() error { _, err := l.SyncContacts(ctx); return err },
		companion.CmdGetAdvertPath:        func() error { _, err := l.AdvertPath(ctx, [32]byte{1}); return err },
		companion.CmdExportContact:        func() error { _, err := l.ExportContact(ctx, [32]byte{1}); return err },
		companion.CmdGetAutoAddConfig:     func() error { _, err := l.AutoAdd(ctx); return err },
		companion.CmdSendPathDiscoveryReq: func() error { return l.DiscoverPath(ctx, [32]byte{1}) },
	} {
		f.mu.Lock()
		f.fail[code] = true
		f.mu.Unlock()
		if err := call(); err == nil {
			t.Errorf("command %s should report the radio's error", CommandName(code))
		}
	}
	// A failed refresh after a path update is quietly skipped.
	f.push(companion.PushPathUpdatedResponse{PublicKey: [32]byte{0xEE}}.ToBytes())
	f.mu.Lock()
	f.fail[companion.CmdSyncNextMessage] = true
	f.mu.Unlock()
	f.push(companion.PushMsgWaitingResponse{}.ToBytes())
	for lg := next[Log](t, l, nil); !strings.Contains(lg.Text, "COULDN'T FETCH MESSAGES"); lg = next[Log](t, l, nil) {
	}
}

func TestConnectFailures(t *testing.T) {
	f := newFakeDevice()
	f.connectErr = errors.New("open serial port: no such file")
	l := startLink(t, f)
	var before []Event
	fatal := next[Fatal](t, l, &before)
	if !strings.Contains(fatal.Err.Error(), "no such file") {
		t.Errorf("fatal = %v", fatal.Err)
	}
	if s, ok := before[len(before)-1].(BootStep); !ok || s.State != StepFail || s.Key != "link" {
		t.Errorf("the link step should fail first: %+v", before)
	}

	f2 := newFakeDevice()
	f2.fail[companion.CmdAppStart] = true
	l2 := startLink(t, f2)
	if fatal := next[Fatal](t, l2, nil); !strings.Contains(fatal.Err.Error(), "companion firmware") {
		t.Errorf("no APP_START answer = %v", fatal.Err)
	}

	// In-sync clocks are left alone; a missing battery reading doesn't stop the sequence.
	f3 := newFakeDevice()
	f3.drift = time.Second
	f3.fail[companion.CmdGetBattAndStorage] = true
	l3 := startLink(t, f3)
	var steps []Event
	next[Ready](t, l3, &steps)
	for _, e := range steps {
		if s, ok := e.(BootStep); ok && s.Key == "clock" && s.State == StepOK && s.Detail != "in sync" {
			t.Errorf("clock = %q", s.Detail)
		}
		if s, ok := e.(BootStep); ok && s.Key == "power" && s.State == StepOK {
			t.Error("power should fail")
		}
	}
	if f3.clockSet {
		t.Error("an in-sync clock shouldn't be set")
	}
}

func TestDropAndReconnect(t *testing.T) {
	f := newFakeDevice()
	f.queue = nil
	l := startLink(t, f)
	next[Ready](t, l, nil)
	f.onDrop()
	if lost := next[Lost](t, l, nil); lost.Err == nil {
		t.Error("lost without a reason")
	}
	f.onRestore()
	next[Restored](t, l, nil)
	next[Ready](t, l, nil) // the sequence runs again after reconnecting
}

func TestNamesAndHelpers(t *testing.T) {
	if CommandName(22) != "DEVICE_QUERY" || CommandName(200) != "CMD_200" {
		t.Error("command names")
	}
	if ResponseName(0x88) != "PUSH_LOG_RX_DATA" || ResponseName(0x77) != "RESP_0x77" {
		t.Error("response names")
	}
	if (SelfInfo{FreqKHz: 869525, BwHz: 250000, SF: 11}).Params() != "869.525 SF11 BW250" {
		t.Error("params should drop a trailing .0")
	}
	for mv, want := range map[uint16]int{3300: 0, 4200: 100, 3750: 50, 2000: 0, 4500: 100} {
		if got := (Battery{mv}).Percent(); got != want {
			t.Errorf("Percent(%d) = %d, want %d", mv, got, want)
		}
	}
	for typ, want := range map[ContactType]string{TypeChat: "Chat", TypeRepeater: "Repeater", TypeRoom: "Room", TypeSensor: "Sensor", 9: "Unknown"} {
		if typ.String() != want {
			t.Errorf("%d = %s", typ, typ.String())
		}
	}
	c := Contact{Key: [32]byte{1, 2, 3, 4, 5, 6, 7}}
	if c.Prefix() != [6]byte{1, 2, 3, 4, 5, 6} || KeyHex([]byte{0xab, 0x01}) != "AB01" {
		t.Error("prefix and hex")
	}
	if _, err := ParseLink("meshcore://zz"); err == nil {
		t.Error("bad hex should fail")
	}
	if _, err := ParseLink("meshcore://0011"); err == nil {
		t.Error("too short should fail")
	}
	if hopsFrom(0xFF) != -1 || hopsFrom(0x43) != 3 {
		t.Error("hop decoding, including 2-byte hashes")
	}
	tcp := NewTCP("10.0.0.4")
	if tcp.Describe() != "10.0.0.4:5000" || tcp.Transport() != "TCP" {
		t.Errorf("tcp = %s %s", tcp.Describe(), tcp.Transport())
	}
	if s := NewSerial("/dev/cu.x"); s.Transport() != "USB" {
		t.Error("serial transport")
	}
}

func TestBatteryPoll(t *testing.T) {
	saved := batteryEvery
	batteryEvery = 10 * time.Millisecond
	t.Cleanup(func() { batteryEvery = saved })
	f := newFakeDevice()
	f.queue = nil
	l := startLink(t, f)
	next[Ready](t, l, nil)
	if b := next[Battery](t, l, nil); b.MilliVolts != 4020 {
		t.Errorf("battery = %+v", b)
	}
}

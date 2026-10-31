package radio

import (
	"context"
	"testing"
	"time"

	"github.com/OwlShack/meshcore-go/companion"
)

func TestDevice(t *testing.T) {
	f := newFakeDevice()
	f.queue = nil
	l := startLink(t, f)
	next[Ready](t, l, nil)
	ctx := context.Background()
	d := l.Device()

	s, err := d.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Self.Name == "" || s.Tuning.RxDelay != 1.2 || s.Pin != 123456 || !s.Radio.Repeat || s.PathHashMode != 1 ||
		len(s.RepeatFreqs) != 2 || s.RepeatFreqs[1].HighKHz != 869650 || s.Scope != "bristol" || !s.HasScope {
		t.Errorf("settings = %+v", s)
	}
	st, err := d.Stats(ctx)
	if err != nil || st.Battery.MilliVolts != 4020 || st.Uptime != 2*time.Hour || st.LastSNR != 6.25 || st.RxAir != 400*time.Second || st.RecvErrors != 3 {
		t.Errorf("stats = %+v %v", st, err)
	}
	vars, _ := d.CustomVars(ctx)
	if len(vars) != 2 || vars[1] != (Var{"sensor.interval", "60"}) {
		t.Errorf("vars = %+v", vars)
	}
	if k, err := d.ExportKey(ctx); err != nil || k[0] != 0x5E {
		t.Errorf("key = %x %v", k[:2], err)
	}
	if c, err := d.Clock(ctx); err != nil || time.Since(c).Abs() > time.Minute {
		t.Errorf("clock = %v %v", c, err)
	}
	if sto, err := d.Storage(ctx); err != nil || sto.Battery.MilliVolts == 0 {
		t.Errorf("storage = %+v %v", sto, err)
	}

	for _, call := range []func() error{
		func() error { return d.SetName(ctx, "MeshCore-k42") },
		func() error { return d.SetLocation(ctx, 51.4545, -2.5879) },
		func() error { return d.SendAdvert(ctx, true) },
		func() error { return d.SendAdvert(ctx, false) },
		func() error { return d.SetRadio(ctx, RadioParams{FreqKHz: 869525, BwHz: 250000, SF: 11, CR: 5}) },
		func() error { return d.SetTxPower(ctx, 20) },
		func() error { return d.SetTuning(ctx, Tuning{RxDelay: 0, AirtimeFactor: 2}) },
		func() error {
			return d.SetOther(ctx, Other{ManualAdd: true, ManualFlags: 4, TelemetryLoc: SharePerContact, MultiAcks: 1})
		},
		func() error { return d.SetPathHashMode(ctx, 2) },
		func() error { return d.SetDefaultScope(ctx, "bath") },
		func() error { return d.SetDefaultScope(ctx, "") },
		func() error { return d.SetPin(ctx, 654321) },
		func() error { return d.ImportKey(ctx, [64]byte{1}) },
		func() error { return d.SetCustomVar(ctx, "gps", "0") },
		func() error { return d.SyncClock(ctx) },
		func() error { return d.Reboot(ctx) },
		func() error { return d.FactoryReset(ctx) },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	var lat companion.SetAdvertLatLonCommand
	var radio companion.SetRadioParamsCommand
	var other companion.SetOtherParamsCommand
	var scopes []companion.SetDefaultFloodScopeCommand
	var adverts []bool
	for _, c := range f.commands() {
		switch c := c.(type) {
		case companion.SetAdvertLatLonCommand:
			lat = c
		case companion.SetRadioParamsCommand:
			radio = c
		case companion.SetOtherParamsCommand:
			other = c
		case companion.SetDefaultFloodScopeCommand:
			scopes = append(scopes, c)
		case companion.SendSelfAdvertCommand:
			adverts = append(adverts, c.Flood)
		}
	}
	if lat.Latitude != 51454500 || lat.Longitude != -2587900 {
		t.Errorf("location = %+v", lat)
	}
	if radio.Frequency != 869525 || radio.Bandwidth != 250000 || radio.SpreadFactor != 11 || radio.Repeat {
		t.Errorf("radio = %+v", radio)
	}
	if other.ManualAddContacts != 5 || other.TelemetryModeLoc != SharePerContact || other.MultiAcks != 1 {
		t.Errorf("other = %+v", other)
	}
	if len(scopes) != 2 || scopes[0].Name != "bath" || len(scopes[0].Key) != 16 || scopes[1].Name != "" {
		t.Errorf("scopes = %+v", scopes)
	}
	if len(adverts) != 2 || !adverts[0] || adverts[1] {
		t.Errorf("adverts = %v", adverts)
	}

	// Failures come back as errors, and Settings still works when the extras are refused.
	f.mu.Lock()
	for _, code := range []byte{companion.CmdGetStats, companion.CmdGetCustomVars, companion.CmdGetTuningParams, companion.CmdGetDefaultFloodScope} {
		f.fail[code] = true
	}
	f.mu.Unlock()
	if _, err := d.Stats(ctx); err == nil {
		t.Error("stats refused")
	}
	if _, err := d.CustomVars(ctx); err == nil {
		t.Error("vars refused")
	}
	if s, err := d.Settings(ctx); err != nil || s.HasScope || s.Tuning != (Tuning{}) {
		t.Errorf("older firmware = %+v %v", s, err)
	}
	f.mu.Lock()
	f.fail[companion.CmdAppStart] = true
	f.mu.Unlock()
	if _, err := d.Settings(ctx); err == nil {
		t.Error("settings refused")
	}
}

func TestParseVars(t *testing.T) {
	if v := ParseVars(""); len(v) != 0 {
		t.Error("empty")
	}
	if v := ParseVars("a:1,junk,:2,b:"); len(v) != 2 || v[1] != (Var{"b", ""}) {
		t.Errorf("vars = %+v", v)
	}
}

func TestChannels(t *testing.T) {
	f := newFakeDevice()
	f.queue = nil
	l := startLink(t, f)
	if r := next[Ready](t, l, nil); len(r.ChannelList) != 2 {
		t.Fatalf("channels at connect = %+v", r.ChannelList)
	}
	ctx := context.Background()
	if err := l.SetChannel(ctx, Channel{Idx: 3, Name: "Family", Secret: [16]byte{7}}); err != nil {
		t.Fatal(err)
	}
	l.SetChannel(ctx, Channel{Idx: 1})
	cs, err := l.Channels(ctx)
	if err != nil || len(cs) != 2 || cs[1] != (Channel{Idx: 3, Name: "Family", Secret: [16]byte{7}}) {
		t.Errorf("channels = %+v %v", cs, err)
	}
	f.mu.Lock()
	f.fail[companion.CmdSetChannel] = true
	f.mu.Unlock()
	if l.SetChannel(ctx, Channel{Idx: 2, Name: "x"}) == nil {
		t.Error("refused")
	}
}

func TestChannelRepliesForAnotherSlotAreDropped(t *testing.T) {
	f := newFakeDevice()
	f.queue, f.stale = nil, map[byte]bool{1: true}
	l := startLink(t, f)
	r := next[Ready](t, l, nil)
	if len(r.ChannelList) != 2 || r.ChannelList[0].Name != "Public" || r.ChannelList[1] != (Channel{Idx: 1, Name: "#bristol", Secret: [16]byte{9}}) {
		t.Errorf("channels = %+v", r.ChannelList)
	}
}

func TestSecretsNeverReachTheFramesPanel(t *testing.T) {
	key := [64]byte{0xDE, 0xAD, 0xBE, 0xEF}
	for _, c := range []struct {
		cmd        []byte
		start, end int
	}{
		{companion.ImportPrivateKeyCommand{PrivateKey: key}.ToBytes(), 1, 65},
		{companion.SetDevicePinCommand{Pin: 246810}.ToBytes(), 1, 5},
		{companion.SendLoginCommand{PublicKey: [32]byte{0xA3}, Password: "hunter2"}.ToBytes(), 33, 40},
		{companion.SetChannelCommand{ChannelIdx: 2, Name: "Family", Secret: [16]byte{7, 7}}.ToBytes(), 34, 50},
		{companion.SendTxtMsgCommand{TxtType: companion.TxtTypeCLIData, PubKeyPrefix: [6]byte{0xA3}, Text: "password hunter2"}.ToBytes(), 13, 29},
		{companion.RunCLICommand{Command: "password hunter2"}.ToBytes(), 1, 17},
	} {
		f := CommandFrame(time.Now(), c.cmd)
		if f.Hidden != [2]int{c.start, c.end} {
			t.Errorf("%s: hidden %v, want [%d %d]", f.Name, f.Hidden, c.start, c.end)
		}
		for i := c.start; i < c.end; i++ {
			if f.Bytes[i] != 0 {
				t.Errorf("%s: byte %d kept", f.Name, i)
				break
			}
		}
		if c.cmd[c.start] == 0 && c.cmd[c.end-1] == 0 {
			t.Errorf("%s: the test should carry a secret", f.Name)
		}
	}
	for _, c := range []struct {
		raw        []byte
		start, end int
	}{
		{companion.PrivateKeyResponse{PrivateKey: key}.ToBytes(), 1, 65},
		{companion.ChannelInfoResponse{ChannelIdx: 2, Name: "Family", Secret: [16]byte{7, 7}}.ToBytes(), 34, 50},
		{companion.ContactMsgRecvV3Response{TxtType: companion.TxtTypeCLIData, Text: "password now: hunter2"}.ToBytes(), 16, 37},
		{companion.ContactMsgRecvResponse{TxtType: companion.TxtTypeCLIData, Text: "password now: hunter2"}.ToBytes(), 13, 34},
		{companion.CLIReplyResponse{Text: "password now: hunter2"}.ToBytes(), 1, 22},
		{companion.DeviceInfoResponse{FirmwareVersion: 9, MaxContacts: 350, MaxChannels: 40, BLEPin: 123456, Model: "T1000"}.ToBytes(), 4, 8},
	} {
		f := ResponseFrame(time.Now(), c.raw)
		if f.Dir != FromRadio || f.Hidden != [2]int{c.start, c.end} {
			t.Errorf("%s: hidden %v, want [%d %d]", f.Name, f.Hidden, c.start, c.end)
		}
		for i := c.start; i < c.end; i++ {
			if f.Bytes[i] != 0 {
				t.Errorf("%s: byte %d kept", f.Name, i)
				break
			}
		}
		if c.raw[c.start] == 0 && c.raw[c.end-1] == 0 {
			t.Errorf("%s: the test should carry a secret", f.Name)
		}
	}
	info := ResponseFrame(time.Now(), companion.DeviceInfoResponse{FirmwareVersion: 9, BLEPin: 123456, Model: "T1000"}.ToBytes())
	if d, err := companion.ParseDeviceInfoResponse(info.Bytes[1:]); err != nil || d.BLEPin != 0 || d.Model != "T1000" {
		t.Errorf("device info: %+v %v, want the PIN gone and the rest kept", d, err)
	}
	dm := companion.ContactMsgRecvV3Response{TxtType: companion.TxtTypePlain, Text: "hello"}.ToBytes()
	if f := ResponseFrame(time.Now(), dm); f.Hidden != [2]int{} || f.Bytes[len(f.Bytes)-1] != 'o' {
		t.Error("an ordinary reply is shown as it is")
	}
	plain := companion.SendTxtMsgCommand{TxtType: companion.TxtTypePlain, Text: "hello"}.ToBytes()
	if f := CommandFrame(time.Now(), plain); f.Hidden != [2]int{} || f.Bytes[len(f.Bytes)-1] != 'o' {
		t.Error("an ordinary message is shown as it is")
	}
}

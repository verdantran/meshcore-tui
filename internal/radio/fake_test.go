package radio

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/companion"
)

// fakeDevice is a companion radio at the protocol level. It parses the real command frames the SDK sends
// and answers with frames from the SDK's firmware-side encoders, so the link is exercised end to end.
type fakeDevice struct {
	mu          sync.Mutex
	handler     func(companion.Response)
	onDrop      func()
	onRestore   func()
	frames      chan []byte
	connectErr  error
	fail        map[byte]bool // command codes that get RESP_CODE_ERR
	self        companion.SelfInfoResponse
	contacts    []companion.ContactResponse
	channels    []companion.ChannelInfoResponse
	queue       [][]byte
	drift       time.Duration
	clockSet    bool
	sent        []any // every parsed command, in order
	autoAdd     companion.AutoAddConfigResponse
	nextAck     uint32
	exportBytes []byte
}

func newFakeDevice() *fakeDevice {
	f := &fakeDevice{frames: make(chan []byte, 256), fail: map[byte]bool{}, drift: 30 * time.Second, nextAck: 0xA0C0}
	f.self = companion.SelfInfoResponse{Name: "MeshCore-k4", PublicKey: [32]byte{0xC0, 0x4F}, RadioFrequency: 869618,
		RadioBandwidth: 62500, RadioSpreadFactor: 8, RadioCodingRate: 8, TxPower: 22, AdvertLatitude: 51440000, AdvertLongitude: -2598000}
	var direct [64]byte
	copy(direct[:], []byte{0xA3, 0x1E})
	f.contacts = []companion.ContactResponse{
		{PublicKey: [32]byte{0x3A, 0xAA}, Type: 1, OutPathLen: 2, OutPath: direct, AdvertName: "Alice", LastAdvert: 1760000000, AdvertLatitude: 51346000, AdvertLongitude: -2970000},
		{PublicKey: [32]byte{0xA3, 0x01}, Type: 2, OutPathLen: 0xFF, AdvertName: "Dundry", LastAdvert: 1760000100},
	}
	f.channels = []companion.ChannelInfoResponse{{ChannelIdx: 0, Name: "Public"}, {ChannelIdx: 1, Name: "#bristol", Secret: [16]byte{9}}}
	f.queue = [][]byte{
		companion.ContactMsgRecvV3Response{SNR: 9.5, PubKeyPrefix: [6]byte{0x3A, 0xAA}, PathLen: 0xFF, TxtType: companion.TxtTypePlain, SenderTimestamp: 1760000200, Text: "ping me"}.ToBytes(),
		companion.ChannelMsgRecvV3Response{SNR: -1.25, ChannelIdx: 1, PathLen: 3, TxtType: companion.TxtTypePlain, SenderTimestamp: 1760000300, Text: "Carol: morning"}.ToBytes(),
	}
	f.autoAdd = companion.AutoAddConfigResponse{Config: companion.AutoAddRepeater | companion.AutoAddOverwriteOldest, MaxHops: 3}
	f.exportBytes = signedAdvertPacket()
	go f.deliver()
	return f
}

// deliver hands frames to the client in order, from its own goroutine as a real transport's read loop does.
func (f *fakeDevice) deliver() {
	for b := range f.frames {
		r, err := companion.ParseResponse(b)
		if err != nil {
			continue
		}
		f.mu.Lock()
		h := f.handler
		f.mu.Unlock()
		if h != nil {
			h(r)
		}
	}
}

func (f *fakeDevice) push(b []byte) { f.frames <- b }

func (f *fakeDevice) Connect(context.Context) error { return f.connectErr }
func (f *fakeDevice) Close() error                  { return nil }
func (f *fakeDevice) SetErrorHandler(func(error))   {}
func (f *fakeDevice) SetDisconnectHandler(h func()) { f.onDrop = h }
func (f *fakeDevice) SetReconnectHandler(h func())  { f.onRestore = h }
func (f *fakeDevice) SetResponseHandler(h func(companion.Response)) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

func (f *fakeDevice) commands() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.sent...)
}

func (f *fakeDevice) Send(cmd []byte) error {
	parsed, err := companion.ParseCommand(cmd)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.sent = append(f.sent, parsed)
	fail := f.fail[cmd[0]]
	f.mu.Unlock()
	if fail {
		f.push(companion.ErrResponse{ErrorCode: companion.ErrCodeNotFound, HasErrorCode: true}.ToBytes())
		return nil
	}
	ok := companion.OkResponse{}.ToBytes()
	switch c := parsed.(type) {
	case companion.AppStartCommand:
		f.push(f.self.ToBytes())
	case companion.DeviceQueryCommand:
		f.push(companion.DeviceInfoResponse{FirmwareVersion: 10, MaxContacts: 350, MaxChannels: 4, Model: "Heltec V4", FirmwareVersionStr: "v1.17.1",
			FirmwareBuildDate: "14 Aug 2026", BLEPin: 123456, RepeatEnabled: true, PathHashMode: 1}.ToBytes())
	case companion.GetDeviceTimeCommand:
		f.push(companion.CurrTimeResponse{Timestamp: uint32(time.Now().Add(-f.drift).Unix())}.ToBytes())
	case companion.SetDeviceTimeCommand:
		f.mu.Lock()
		f.clockSet = true
		f.mu.Unlock()
		f.push(ok)
	case companion.GetContactsCommand:
		f.mu.Lock()
		list := append([]companion.ContactResponse(nil), f.contacts...)
		f.mu.Unlock()
		f.push(companion.ContactsStartResponse{Count: uint32(len(list)), HasCount: true}.ToBytes())
		for _, ct := range list {
			f.push(ct.ToBytes())
		}
		f.push(companion.EndOfContactsResponse{MostRecentLastmod: 1760000000}.ToBytes())
	case companion.GetChannelCommand:
		f.mu.Lock()
		var ch companion.ChannelInfoResponse
		have := int(c.ChannelIdx) < len(f.channels)
		if have {
			ch = f.channels[c.ChannelIdx]
		}
		f.mu.Unlock()
		switch {
		case have:
			f.push(ch.ToBytes())
		case c.ChannelIdx < 4: // firmware answers every slot it has, named or not
			f.push(companion.ChannelInfoResponse{ChannelIdx: c.ChannelIdx}.ToBytes())
		default:
			f.push(companion.ErrResponse{ErrorCode: companion.ErrCodeNotFound, HasErrorCode: true}.ToBytes())
		}
	case companion.SetChannelCommand:
		f.mu.Lock()
		for len(f.channels) <= int(c.ChannelIdx) {
			f.channels = append(f.channels, companion.ChannelInfoResponse{ChannelIdx: byte(len(f.channels))})
		}
		f.channels[c.ChannelIdx] = companion.ChannelInfoResponse{ChannelIdx: c.ChannelIdx, Name: c.Name, Secret: c.Secret}
		f.mu.Unlock()
		f.push(ok)
	case companion.SyncNextMessageCommand:
		f.mu.Lock()
		var next []byte
		if len(f.queue) > 0 {
			next, f.queue = f.queue[0], f.queue[1:]
		}
		f.mu.Unlock()
		if next == nil {
			next = companion.NoMoreMessagesResponse{}.ToBytes()
		}
		f.push(next)
	case companion.GetBattAndStorageCommand:
		f.push(companion.BattAndStorageResponse{BatteryMilliVolts: 4020, UsedStorageKB: 100, TotalStorageKB: 1000}.ToBytes())
	case companion.SendTxtMsgCommand:
		f.mu.Lock()
		f.nextAck++
		ack := f.nextAck
		f.mu.Unlock()
		// The 10-byte form current firmware sends: flood flag, expected ack, timeout.
		f.push(companion.SentResponse{HasExtended: true, IsFlood: false, Tag: ack, EstTimeout: 4200}.ToBytes())
		if c.TxtType == companion.TxtTypeCLIData {
			// A repeater answers a console command with a CLI-typed message of its own.
			f.mu.Lock()
			f.queue = append(f.queue, companion.ContactMsgRecvV3Response{PubKeyPrefix: c.PubKeyPrefix, PathLen: 0xFF,
				TxtType: companion.TxtTypeCLIData, Text: "> " + c.Text}.ToBytes())
			f.mu.Unlock()
			f.push(companion.PushMsgWaitingResponse{}.ToBytes())
		}
	case companion.SendLoginCommand:
		f.push(companion.SentResponse{HasExtended: true, Tag: 1, EstTimeout: 5000}.ToBytes())
		prefix := [6]byte(c.PublicKey[:6])
		switch c.Password {
		case "admin":
			f.push(companion.PushLoginSuccessResponse{Permissions: 1, PubKeyPrefix: prefix, HasServerInfo: true, ServerTime: 1, ACL: 3, FirmwareLevel: 1}.ToBytes())
		case "guest":
			f.push(companion.PushLoginSuccessResponse{Permissions: 0, PubKeyPrefix: prefix}.ToBytes())
		default:
			f.push(companion.PushLoginFailResponse{PubKeyPrefix: prefix}.ToBytes())
		}
	case companion.LogoutCommand:
		f.push(ok)
	case companion.SendStatusReqCommand:
		f.push(companion.SentResponse{HasExtended: true, Tag: 2, EstTimeout: 5000}.ToBytes())
		stats := meshcore.BuildRepeaterStats(meshcore.RepeaterStats{CommonStats: meshcore.CommonStats{BattMilliVolts: 4110, NoiseFloor: -112, UpTimeSecs: 1051200}})
		f.push(companion.PushStatusResp{PubKeyPrefix: [6]byte(c.PublicKey[:6]), StatusData: stats}.ToBytes())
	case companion.SendTelemetryReqCommand:
		f.push(companion.SentResponse{HasExtended: true, Tag: 3, EstTimeout: 5000}.ToBytes())
		lpp := meshcore.NewLPPEncoder()
		lpp.AddVoltage(1, 4.11)
		f.push(companion.PushTelemetryResp{PubKeyPrefix: [6]byte(c.PublicKey[:6]), LPPData: lpp.Bytes()}.ToBytes())
	case companion.SendBinaryReqCommand:
		f.push(companion.SentResponse{HasExtended: true, Tag: 0x51, EstTimeout: 5000}.ToBytes())
		f.push(companion.PushBinaryResp{Tag: 0x51, ResponseData: append([]byte{0xEE}, c.RequestData...)}.ToBytes())
	case companion.SendTracePathCommand, companion.SendPathDiscoveryReqCommand:
		f.push(companion.SentResponse{HasExtended: true, IsFlood: true, Tag: 1, EstTimeout: 9000}.ToBytes())
	case companion.GetAdvertPathCommand:
		f.push(companion.AdvertPathResponse{RecvTimestamp: 1760000400, PathLen: 2, Path: []byte{0xE1, 0xB2}}.ToBytes())
	case companion.ExportContactCommand:
		f.push(companion.ExportContactResponse{AdvertData: f.exportBytes}.ToBytes())
	case companion.GetAutoAddConfigCommand:
		f.push(f.autoAdd.ToBytes())
	case companion.SetAutoAddConfigCommand:
		f.mu.Lock()
		f.autoAdd = companion.AutoAddConfigResponse{Config: c.Config, MaxHops: c.MaxHops}
		f.mu.Unlock()
		f.push(ok)
	case companion.GetContactByKeyCommand:
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, ct := range f.contacts {
			if ct.PublicKey == c.PublicKey {
				f.push(ct.ToBytes())
				return nil
			}
		}
		f.push(companion.ErrResponse{ErrorCode: companion.ErrCodeNotFound, HasErrorCode: true}.ToBytes())
	case companion.AddUpdateContactCommand, companion.RemoveContactCommand, companion.ResetPathCommand,
		companion.ImportContactCommand, companion.SendChannelTxtMsgCommand,
		companion.SetAdvertNameCommand, companion.SetAdvertLatLonCommand, companion.SendSelfAdvertCommand, companion.SetRadioParamsCommand,
		companion.SetTxPowerCommand, companion.SetTuningParamsCommand, companion.SetOtherParamsCommand, companion.SetPathHashModeCommand,
		companion.SetDefaultFloodScopeCommand, companion.SetDevicePinCommand, companion.ImportPrivateKeyCommand, companion.SetCustomVarCommand:
		f.push(ok)
	case companion.RebootCommand, companion.FactoryResetCommand:
		// The radio goes away without a reply.
	case companion.GetTuningParamsCommand:
		f.push(companion.TuningParamsResponse{RxDelayBase: 1.2, AirtimeFactor: 1}.ToBytes())
	case companion.GetAllowedRepeatFreqCommand:
		f.push(companion.AllowedRepeatFreqResponse{Ranges: []companion.FreqRange{{LowerFreq: 433000, UpperFreq: 433500}, {LowerFreq: 869400, UpperFreq: 869650}}}.ToBytes())
	case companion.GetDefaultFloodScopeCommand:
		f.push(companion.DefaultFloodScopeResponse{Name: "#bristol", Key: make([]byte, 16)}.ToBytes())
	case companion.ExportPrivateKeyCommand:
		f.push(companion.PrivateKeyResponse{PrivateKey: [64]byte{0x5E, 0xC2}}.ToBytes())
	case companion.GetCustomVarsCommand:
		f.push(companion.CustomVarsResponse{Vars: "gps:1,sensor.interval:60"}.ToBytes())
	case companion.GetStatsCommand:
		r := companion.StatsResponse{StatsType: c.StatsType}
		switch c.StatsType {
		case companion.StatsTypeCore:
			r.Core = &companion.CoreStats{BatteryMV: 4020, UptimeSecs: 7200, QueueLen: 1}
		case companion.StatsTypeRadio:
			r.Radio = &companion.RadioStats{NoiseFloor: -112, LastRSSI: -90, LastSNR: 6.25, TxAirSecs: 30, RxAirSecs: 400}
		default:
			r.Packets = &companion.PacketStats{PacketsRecv: 900, PacketsSent: 40, SentFlood: 30, SentDirect: 10, RecvFlood: 700, RecvDirect: 200, RecvErrors: 3}
		}
		f.push(r.ToBytes())
	default:
		return errors.New("fake device: unhandled command")
	}
	return nil
}

// signedAdvertPacket is what a radio exports for a contact: a whole advert packet, signed.
func signedAdvertPacket() []byte {
	seed := sha256.Sum256([]byte("alice"))
	adv, err := meshcore.NewAdvert(meshcore.NewLocalIdentityFromSeed(seed), meshcore.AdvertAppData{Type: "CHAT", Name: "Alice"}, 1760000000)
	if err != nil {
		panic(err)
	}
	payload, _ := adv.ToBytes()
	raw, err := (&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, 0), Payload: payload}).ToBytes()
	if err != nil {
		panic(err)
	}
	return raw
}

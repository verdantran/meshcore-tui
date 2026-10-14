package radio

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/companion"
	"github.com/OwlShack/meshcore-go/companion/client"
	"github.com/OwlShack/meshcore-go/companion/transport"
)

const (
	appName       = "meshcore-tui"
	appVersion    = 3
	stepTimeout   = 6 * time.Second
	clockDriftMax = 5 * time.Second
)

// batteryEvery is how often the battery is read; tests shorten it.
var batteryEvery = 60 * time.Second

// sdkTransport is the SDK's serial and TCP transports, which also report drops and reconnects.
type sdkTransport interface {
	client.Transport
	SetDisconnectHandler(func())
	SetReconnectHandler(func())
}

// Link is a Radio backed by the meshcore-go SDK.
type Link struct {
	describe, kind string
	tap            *tap
	raw            sdkTransport
	client         *client.Client
	events         chan Event
	cancel         context.CancelFunc
	closeOnce      sync.Once
}

// quiet keeps the SDK's own logging off the terminal the UI is drawing on.
var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func NewSerial(port string) *Link {
	t := transport.NewSerialTransport(transport.SerialConfig{Port: port, BaudRate: 115200, BaseConfig: transport.BaseConfig{Logger: quiet}})
	return newLink(port, "USB", t)
}

func NewTCP(addr string) *Link {
	if !strings.Contains(addr, ":") {
		addr += ":5000"
	}
	t := transport.NewTCPTransport(transport.TCPConfig{Address: addr, BaseConfig: transport.BaseConfig{Logger: quiet}})
	return newLink(addr, "TCP", t)
}

func newLink(describe, kind string, t sdkTransport) *Link {
	l := &Link{describe: describe, kind: kind, raw: t, events: make(chan Event, 256)}
	l.tap = &tap{inner: t, emit: l.emit}
	l.client = client.New(l.tap)
	return l
}

func (l *Link) Events() <-chan Event { return l.events }
func (l *Link) Describe() string     { return l.describe }
func (l *Link) Transport() string    { return l.kind }

func (l *Link) Close() error {
	var err error
	l.closeOnce.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
		err = l.client.Close()
	})
	return err
}

// emit never blocks the SDK's goroutines; if the UI falls badly behind, the oldest detail is dropped.
func (l *Link) emit(e Event) {
	select {
	case l.events <- e:
	default:
	}
}

func (l *Link) Start(ctx context.Context) {
	ctx, l.cancel = context.WithCancel(ctx)
	l.registerPushes()
	l.raw.SetDisconnectHandler(func() { l.emit(Lost{Err: errors.New("connection dropped")}) })
	l.raw.SetReconnectHandler(func() {
		l.emit(Restored{})
		go l.sync(ctx)
	})
	go func() {
		l.step("link", "LINK", l.describe, StepWait)
		if err := l.client.Connect(ctx); err != nil {
			l.step("link", "LINK", err.Error(), StepFail)
			l.emit(Fatal{Err: err})
			return
		}
		l.step("link", "LINK", l.describe, StepOK)
		l.sync(ctx)
		go l.pollBattery(ctx)
	}()
}

func (l *Link) step(key, label, detail string, s StepState) {
	l.emit(BootStep{Key: key, Label: label, Detail: detail, State: s})
}

// run times out one connect step and reports it.
func run[T any](l *Link, ctx context.Context, key, label string, f func(context.Context) (T, error), detail func(T) string) (T, bool) {
	l.step(key, label, "", StepWait)
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	v, err := f(sctx)
	if err != nil {
		l.step(key, label, err.Error(), StepFail)
		return v, false
	}
	l.step(key, label, detail(v), StepOK)
	return v, true
}

// sync is the connect sequence: identify, check the clock, read contacts and channels, then drain
// messages that queued while we were away. They're delivered after Ready, when the UI can store them.
func (l *Link) sync(ctx context.Context) {
	c := l.client
	self, ok := run(l, ctx, "hello", "HELLO", func(ctx context.Context) (companion.SelfInfoResponse, error) {
		return c.AppStart(ctx, appVersion, appName)
	}, func(s companion.SelfInfoResponse) string { return s.Name })
	if !ok {
		l.emit(Fatal{Err: errors.New("the radio didn't answer APP_START; is this companion firmware?")})
		return
	}
	dev, _ := run(l, ctx, "device", "DEVICE", c.DeviceQuery, func(d companion.DeviceInfoResponse) string {
		return fmt.Sprintf("%s · fw %s", d.Model, d.FirmwareVersionStr)
	})
	run(l, ctx, "clock", "CLOCK", func(ctx context.Context) (string, error) {
		t, err := c.GetDeviceTime(ctx)
		if err != nil {
			return "", err
		}
		drift := time.Since(time.Unix(int64(t.Timestamp), 0)).Round(time.Second)
		if drift.Abs() <= clockDriftMax {
			return "in sync", nil
		}
		if err := c.SyncDeviceTime(ctx); err != nil {
			return "", err
		}
		return fmt.Sprintf("synced, was %+v off", drift), nil
	}, func(s string) string { return s })
	contacts, _ := run(l, ctx, "contacts", "CONTACTS", c.GetContacts, func(cs []companion.ContactResponse) string {
		return fmt.Sprintf("%d synced", len(cs))
	})
	channels, _ := run(l, ctx, "channels", "CHANNELS", func(ctx context.Context) ([]Channel, error) {
		var out []Channel
		for i := 0; i < max(int(dev.MaxChannels), 1); i++ {
			ch, err := c.GetChannel(ctx, byte(i))
			if err != nil {
				break
			}
			if ch.Name != "" {
				out = append(out, Channel{Idx: int(ch.ChannelIdx), Name: ch.Name, Secret: ch.Secret})
			}
		}
		return out, nil
	}, func(cs []Channel) string { return fmt.Sprintf("%d configured", len(cs)) })
	waiting, _ := run(l, ctx, "queue", "QUEUE", c.GetWaitingMessages, func(ms []client.WaitingMessage) string {
		return fmt.Sprintf("%d received", len(ms))
	})
	batt, _ := run(l, ctx, "power", "POWER", c.GetBattAndStorage, func(b companion.BattAndStorageResponse) string {
		return fmt.Sprintf("%.2f V · %d%%", float64(b.BatteryMilliVolts)/1000, Battery{b.BatteryMilliVolts}.Percent())
	})

	list := make([]Contact, len(contacts))
	for i, ct := range contacts {
		list[i] = contactFrom(ct.PublicKey, ct.AdvertName, ct.Type, ct.Flags, ct.OutPathLen, ct.OutPath[:], ct.LastAdvert, ct.AdvertLatitude, ct.AdvertLongitude)
	}
	l.emit(Ready{
		Self: SelfInfo{
			Name: self.Name, PubKey: self.PublicKey, FreqKHz: self.RadioFrequency, BwHz: self.RadioBandwidth,
			SF: self.RadioSpreadFactor, CR: self.RadioCodingRate, TxPower: self.TxPower,
			Lat: float64(self.AdvertLatitude) / 1e6, Lon: float64(self.AdvertLongitude) / 1e6,
		},
		Device:      DeviceInfo{Model: dev.Model, Firmware: dev.FirmwareVersionStr, BuildDate: dev.FirmwareBuildDate, MaxContacts: int(dev.MaxContacts), MaxChannels: int(dev.MaxChannels)},
		Contacts:    len(contacts),
		Channels:    len(channels),
		Battery:     Battery{batt.BatteryMilliVolts},
		ContactList: list,
		ChannelList: channels,
	})
	l.deliver(waiting)
}

func contactFrom(key [32]byte, name string, typ, flags, pathLen byte, path []byte, lastAdvert uint32, lat, lon int32) Contact {
	c := Contact{Key: key, Name: name, Type: ContactType(typ), Flags: flags, PathLen: -1,
		LastAdvert: time.Unix(int64(lastAdvert), 0), Lat: float64(lat) / 1e6, Lon: float64(lon) / 1e6}
	if pathLen != 0xFF {
		size, hops := meshcore.PathLenFields(pathLen)
		n := min(int(size)*int(hops), len(path))
		c.PathLen, c.Path = int(hops), append([]byte(nil), path[:n]...)
	}
	return c
}

// hopsFrom reads a received message's path_len: 0xFF means it came over a direct route.
func hopsFrom(pathLen byte) int {
	if pathLen == 0xFF {
		return -1
	}
	_, hops := meshcore.PathLenFields(pathLen)
	return int(hops)
}

func (l *Link) deliver(ms []client.WaitingMessage) {
	now := time.Now()
	for _, w := range ms {
		in := Incoming{At: now, SNR: w.SNR, HasSNR: w.HasSNR}
		switch {
		case w.Contact != nil:
			m := w.Contact
			in.From, in.Hops, in.Text = m.PubKeyPrefix, hopsFrom(m.PathLen), m.Text
			in.SentAt, in.Signed = time.Unix(int64(m.SenderTimestamp), 0), m.TxtType == companion.TxtTypeSignedPlain
		case w.Channel != nil:
			m := w.Channel
			in.Channel, in.ChannelIdx, in.Hops, in.Text = true, int(m.ChannelIdx), hopsFrom(m.PathLen), m.Text
			in.SentAt = time.Unix(int64(m.SenderTimestamp), 0)
		default:
			continue // binary channel data isn't shown yet
		}
		l.emit(in)
	}
}

// drain fetches queued messages after the radio says some are waiting.
func (l *Link) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()
	ms, err := l.client.GetWaitingMessages(ctx)
	if err != nil {
		l.emit(Log{At: time.Now(), Text: "COULDN'T FETCH MESSAGES  " + err.Error(), Tone: ToneWarn})
	}
	l.deliver(ms)
}

func (l *Link) refreshContact(key [32]byte) {
	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()
	ct, err := l.client.GetContactByKey(ctx, meshcore.NewIdentity(key))
	if err != nil {
		return
	}
	l.emit(ContactUpdated{Contact: contactFrom(ct.PublicKey, ct.AdvertName, ct.Type, ct.Flags, ct.OutPathLen, ct.OutPath[:], ct.LastAdvert, ct.AdvertLatitude, ct.AdvertLongitude)})
}

func (l *Link) SendText(ctx context.Context, to [32]byte, text string) (Sent, error) {
	r, err := l.client.SendTextMessage(ctx, meshcore.NewIdentity(to), text, companion.TxtTypePlain)
	if err != nil {
		return Sent{}, err
	}
	// Current firmware sends the 10-byte form, with the expected ack code in the field the SDK calls Tag.
	ack := r.AckCode
	if r.HasExtended {
		ack = r.Tag
	}
	return Sent{Ack: ack, Flood: r.IsFlood, Timeout: time.Duration(r.EstTimeout) * time.Millisecond}, nil
}

func (l *Link) SendChannel(ctx context.Context, idx int, text string) error {
	_, err := l.client.SendChannelTextMessage(ctx, byte(idx), text, companion.TxtTypePlain)
	return err
}

func (l *Link) ResetPath(ctx context.Context, contact [32]byte) error {
	return l.client.ResetPath(ctx, meshcore.NewIdentity(contact))
}

func (l *Link) pollBattery(ctx context.Context) {
	t := time.NewTicker(batteryEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sctx, cancel := context.WithTimeout(ctx, stepTimeout)
			b, err := l.client.GetBattAndStorage(sctx)
			cancel()
			if err == nil {
				l.emit(Battery{b.BatteryMilliVolts})
			}
		}
	}
}

func hexPrefix(b []byte) string { return strings.ToUpper(fmt.Sprintf("%x", b)) }

func (l *Link) registerPushes() {
	c := l.client
	c.OnPush(companion.PushLogRxData, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushLogRxDataResponse); ok {
			l.emit(Packet{At: time.Now(), SNR: p.LastSNR, RSSI: p.LastRSSI, Raw: p.Raw})
		}
	})
	// NEW_ADVERT is a node that is *not* a contact: auto-add was off for it, it was too many hops away,
	// or the table was full. ADVERT is a contact whose details changed, including one just auto-added.
	c.OnPush(companion.PushNewAdvert, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushNewAdvertResponse); ok {
			l.emit(Heard{Contact: contactFrom(p.PublicKey, p.AdvertName, p.Type, p.Flags, p.OutPathLen, p.OutPath[:], p.LastAdvert, p.AdvertLatitude, p.AdvertLongitude)})
			l.emit(Log{At: time.Now(), Text: fmt.Sprintf("HEARD  %s [%s], not a contact", p.AdvertName, hexPrefix(p.PublicKey[:1]))})
		}
	})
	c.OnPush(companion.PushAdvert, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushAdvertResponse); ok {
			l.emit(Log{At: time.Now(), Text: fmt.Sprintf("ADVERT  %s", hexPrefix(p.PublicKey[:6]))})
			go l.refreshContact(p.PublicKey)
		}
	})
	c.OnPush(companion.PushContactDeleted, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushContactDeletedResponse); ok {
			l.emit(ContactRemoved{Key: p.PublicKey})
		}
	})
	c.OnPush(companion.PushTraceData, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushTraceDataResponse); ok {
			snrs := make([]float32, len(p.PathSnrs))
			for i, b := range p.PathSnrs {
				snrs[i] = meshcore.PathSNRdB(b)
			}
			l.emit(TraceResult{Tag: p.Tag, Hashes: p.PathHashes, SNRs: snrs, LastSNR: p.LastSNR})
		}
	})
	c.OnPush(companion.PushPathDiscoveryResponse, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushPathDiscoveryResp); ok {
			l.emit(PathFound{Prefix: p.PubKeyPrefix, Out: p.OutPath, In: p.InPath})
		}
	})
	c.OnPush(companion.PushPathUpdated, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushPathUpdatedResponse); ok {
			l.emit(Log{At: time.Now(), Text: fmt.Sprintf("PATH UPDATED  %s", hexPrefix(p.PublicKey[:6]))})
			go l.refreshContact(p.PublicKey) // handlers run on the SDK's dispatch goroutine, so commands go elsewhere
		}
	})
	c.OnPush(companion.PushSendConfirmed, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushSendConfirmedResponse); ok {
			l.emit(Acked{Ack: p.AckCode, RTT: time.Duration(p.RoundTrip) * time.Millisecond})
		}
	})
	c.OnPush(companion.PushMsgWaiting, func(companion.Response) { go l.drain() })
	c.OnPush(companion.PushContactsFull, func(companion.Response) { l.emit(ContactsFull{}) })
}

// tap passes frames through to the SDK transport, copying each one to the debug pane.
type tap struct {
	inner client.Transport
	emit  func(Event)
}

func (t *tap) Connect(ctx context.Context) error { return t.inner.Connect(ctx) }
func (t *tap) Close() error                      { return t.inner.Close() }
func (t *tap) SetErrorHandler(h func(error))     { t.inner.SetErrorHandler(h) }

func (t *tap) Send(cmd []byte) error {
	if len(cmd) > 0 {
		now := time.Now()
		t.emit(Frame{At: now, Dir: ToRadio, Code: cmd[0], Name: CommandName(cmd[0]), Bytes: append([]byte(nil), cmd...)})
		if overAir[cmd[0]] {
			t.emit(Transmit{At: now, What: CommandName(cmd[0])})
		}
	}
	return t.inner.Send(cmd)
}

func (t *tap) SetResponseHandler(h func(companion.Response)) {
	t.inner.SetResponseHandler(func(r companion.Response) {
		t.emit(Frame{At: time.Now(), Dir: FromRadio, Code: r.Code, Name: ResponseName(r.Code)})
		h(r)
	})
}

// overAir lists commands that make the radio transmit.
var overAir = map[byte]bool{
	companion.CmdSendTxtMsg: true, companion.CmdSendChannelTxtMsg: true, companion.CmdSendSelfAdvert: true,
	companion.CmdSendRawData: true, companion.CmdSendLogin: true, companion.CmdSendStatusReq: true,
	companion.CmdSendTracePath: true, companion.CmdSendTelemetryReq: true, companion.CmdSendBinaryReq: true,
	companion.CmdSendPathDiscoveryReq: true, companion.CmdSendControlData: true, companion.CmdSendAnonReq: true,
	companion.CmdSendChannelData: true, companion.CmdSendRawPacket: true,
}

func (l *Link) SyncContacts(ctx context.Context) ([]Contact, error) {
	cs, err := l.client.GetContacts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Contact, len(cs))
	for i, ct := range cs {
		out[i] = contactFrom(ct.PublicKey, ct.AdvertName, ct.Type, ct.Flags, ct.OutPathLen, ct.OutPath[:], ct.LastAdvert, ct.AdvertLatitude, ct.AdvertLongitude)
	}
	return out, nil
}

// SaveContact writes a whole contact record; the radio adds it if it's new.
func (l *Link) SaveContact(ctx context.Context, c Contact) error {
	cmd := companion.AddUpdateContactCommand{
		PublicKey: c.Key, Type: byte(c.Type), Flags: c.Flags, OutPathLen: 0xFF, Name: c.Name,
		LastAdvert: uint32(c.LastAdvert.Unix()), Latitude: int32(c.Lat * 1e6), Longitude: int32(c.Lon * 1e6),
		LastModified: uint32(time.Now().Unix()),
	}
	if c.PathLen >= 0 {
		cmd.OutPathLen, cmd.OutPath = meshcore.MakePathLen(1, uint8(len(c.Path))), c.Path
	}
	return l.client.AddUpdateContactFull(ctx, cmd)
}

func (l *Link) RemoveContact(ctx context.Context, key [32]byte) error {
	return l.client.RemoveContact(ctx, meshcore.NewIdentity(key))
}

// Trace sends a trace along path (1-byte hop hashes); the tag matches the TraceResult that comes back.
func (l *Link) Trace(ctx context.Context, path []byte) (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	tag := binary.LittleEndian.Uint32(b[:])
	_, err := l.client.SendTracePath(ctx, tag, 0, 0, path)
	return tag, err
}

func (l *Link) DiscoverPath(ctx context.Context, key [32]byte) error {
	_, err := l.client.SendPathDiscoveryReq(ctx, meshcore.NewIdentity(key))
	return err
}

func (l *Link) AdvertPath(ctx context.Context, key [32]byte) (AdvertPath, error) {
	r, err := l.client.GetAdvertPath(ctx, meshcore.NewIdentity(key))
	if err != nil {
		return AdvertPath{}, err
	}
	return AdvertPath{At: time.Unix(int64(r.RecvTimestamp), 0), Path: r.Path}, nil
}

func (l *Link) ExportContact(ctx context.Context, key [32]byte) (string, error) {
	r, err := l.client.ExportContact(ctx, meshcore.NewIdentity(key))
	if err != nil {
		return "", err
	}
	return LinkPrefix + hex.EncodeToString(r.AdvertData), nil
}

func (l *Link) ImportContact(ctx context.Context, link string) error {
	data, err := ParseLink(link)
	if err != nil {
		return err
	}
	return l.client.ImportContact(ctx, data)
}

// ParseLink reads a meshcore:// contact link back into the advert bytes it carries.
func ParseLink(link string) ([]byte, error) {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, LinkPrefix) {
		return nil, errors.New("not a meshcore:// link")
	}
	data, err := hex.DecodeString(strings.TrimPrefix(link, LinkPrefix))
	if err != nil || len(data) < 32 {
		return nil, errors.New("the link is damaged or incomplete")
	}
	return data, nil
}

func (l *Link) AutoAdd(ctx context.Context) (AutoAdd, error) {
	r, err := l.client.GetAutoAddConfig(ctx)
	if err != nil {
		return AutoAdd{}, err
	}
	return AutoAdd{
		Chat: r.Config&companion.AutoAddChat != 0, Repeater: r.Config&companion.AutoAddRepeater != 0,
		Room: r.Config&companion.AutoAddRoomServer != 0, Sensor: r.Config&companion.AutoAddSensor != 0,
		OverwriteOldest: r.Config&companion.AutoAddOverwriteOldest != 0, MaxHops: int(r.MaxHops),
	}, nil
}

func (l *Link) SetAutoAdd(ctx context.Context, a AutoAdd) error {
	var cfg byte
	for bit, on := range map[byte]bool{companion.AutoAddChat: a.Chat, companion.AutoAddRepeater: a.Repeater,
		companion.AutoAddRoomServer: a.Room, companion.AutoAddSensor: a.Sensor, companion.AutoAddOverwriteOldest: a.OverwriteOldest} {
		if on {
			cfg |= bit
		}
	}
	return l.client.SetAutoAddConfig(ctx, cfg, byte(min(max(a.MaxHops, 0), 64)))
}

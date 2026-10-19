package radio

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/companion"
	"github.com/OwlShack/meshcore-go/companion/client"

	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

const (
	appName       = "meshcore-tui"
	appVersion    = 3
	stepTimeout   = 6 * time.Second
	fetchTimeout  = 30 * time.Second // long, because the fetch queues behind whatever command is running
	fetchTries    = 3
	silentPolls   = 2 // battery polls unanswered before the link is redialled
	clockDriftMax = 5 * time.Second
)

// batteryEvery is how often the battery is read; tests shorten it.
var batteryEvery = 60 * time.Second

// pollTimeout bounds each battery poll; tests shorten it.
var pollTimeout = stepTimeout

// fetchRetry is the pause before fetching again after a failure; tests shorten it.
var fetchRetry = 3 * time.Second

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
	slots          atomic.Int32 // how many channels the radio holds, from DEVICE_QUERY
	waiting        chan struct{}

	pendMu   sync.Mutex
	pending  []Event
	light    int // droppable events in pending
	dropped  int
	lostMsgs int
	kick     chan struct{}
	done     chan struct{}

	refreshMu  sync.Mutex
	refreshing map[[32]byte]bool
	refreshQ   [][32]byte
	refreshNow chan struct{}
}

func NewSerial(port string) *Link {
	return newLink(port, "USB", serialStream(port))
}

func NewTCP(addr string) *Link {
	if !strings.Contains(addr, ":") {
		addr += ":5000"
	}
	return newLink(addr, "TCP", tcpStream(addr))
}

func newLink(describe, kind string, t sdkTransport) *Link {
	l := &Link{describe: describe, kind: kind, raw: t, events: make(chan Event, 256), waiting: make(chan struct{}, 1),
		kick: make(chan struct{}, 1), done: make(chan struct{}), refreshing: map[[32]byte]bool{}, refreshNow: make(chan struct{}, 1)}
	go l.pump()
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
		close(l.done)
		err = l.client.Close()
	})
	return err
}

// maxPending bounds the backlog of droppable events when the UI falls behind; maxBacklog bounds everything.
const (
	maxPending = 4096
	maxBacklog = 1 << 16
)

func droppable(e Event) bool {
	switch e.(type) {
	case Frame, Packet, Transmit, Heard, Log, AdvertPath:
		return true
	}
	return false
}

// emit never blocks the reading goroutines; pump hands events on in order. Only a backlog past
// maxBacklog loses messages, and pump then logs how many.
func (l *Link) emit(e Event) {
	l.pendMu.Lock()
	switch {
	case len(l.pending) >= maxBacklog:
		if _, ok := e.(Incoming); ok {
			l.lostMsgs++
		} else {
			l.dropped++
		}
		l.pendMu.Unlock()
		return
	case droppable(e):
		if l.light >= maxPending {
			l.dropped++
			l.pendMu.Unlock()
			return
		}
		l.light++
	}
	l.pending = append(l.pending, e)
	l.pendMu.Unlock()
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

func (l *Link) pump() {
	for {
		select {
		case <-l.done:
			return
		case <-l.kick:
		}
		l.pendMu.Lock()
		batch := l.pending
		dropped, lost := l.dropped, l.lostMsgs
		l.pending, l.light, l.dropped, l.lostMsgs = nil, 0, 0, 0
		l.pendMu.Unlock()
		if lost > 0 {
			batch = append(batch, Log{At: time.Now(), Text: fmt.Sprintf("DROPPED  %d messages while the screen was busy", lost), Tone: ToneFail})
		}
		if dropped > 0 {
			batch = append(batch, Log{At: time.Now(), Text: fmt.Sprintf("DROPPED  %d events while the screen was busy", dropped), Tone: ToneWarn})
		}
		for _, e := range batch {
			select {
			case l.events <- e:
			case <-l.done:
				return
			}
		}
	}
}

func (l *Link) Start(ctx context.Context) {
	ctx, l.cancel = context.WithCancel(ctx)
	l.registerPushes()
	go l.refresher(ctx)
	l.raw.SetDisconnectHandler(func() { l.emit(Lost{Err: errors.New("connection dropped")}) })
	l.raw.SetReconnectHandler(func() {
		l.emit(Restored{})
		go l.sync(ctx)
	})
	go func() {
		l.step("link", "LINK", l.describe, StepWait)
		if err := l.client.Connect(ctx); err != nil {
			l.step("link", "LINK", untrusted.Line(err.Error()), StepFail)
			l.emit(Fatal{Err: err})
			return
		}
		l.step("link", "LINK", l.describe, StepOK)
		l.sync(ctx)
		go l.pollBattery(ctx)
		go l.fetchLoop(ctx)
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
		l.step(key, label, untrusted.Line(err.Error()), StepFail)
		return v, false
	}
	l.step(key, label, untrusted.Line(detail(v)), StepOK)
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
	l.slots.Store(int32(dev.MaxChannels))
	channels, _ := run(l, ctx, "channels", "CHANNELS", l.Channels, func(cs []Channel) string { return fmt.Sprintf("%d configured", len(cs)) })
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
			Name: untrusted.Line(self.Name), PubKey: self.PublicKey, FreqKHz: self.RadioFrequency, BwHz: self.RadioBandwidth,
			SF: self.RadioSpreadFactor, CR: self.RadioCodingRate, TxPower: self.TxPower,
			Lat: float64(self.AdvertLatitude) / 1e6, Lon: float64(self.AdvertLongitude) / 1e6,
		},
		Device: DeviceInfo{Model: untrusted.Line(dev.Model), Firmware: untrusted.Line(dev.FirmwareVersionStr), BuildDate: untrusted.Line(dev.FirmwareBuildDate), MaxContacts: int(dev.MaxContacts), MaxChannels: int(dev.MaxChannels),
			PinSet: dev.FirmwareVersion >= 3 && dev.BLEPin != 0, PathHash: int(dev.PathHashMode) + 1},
		Contacts:    len(contacts),
		Channels:    len(channels),
		Battery:     Battery{batt.BatteryMilliVolts},
		ContactList: list,
		ChannelList: channels,
	})
	l.deliver(waiting, true)
}

func contactFrom(key [32]byte, name string, typ, flags, pathLen byte, path []byte, lastAdvert uint32, lat, lon int32) Contact {
	c := Contact{Key: key, Name: untrusted.Line(name), Type: ContactType(typ), Flags: flags, PathLen: -1,
		LastAdvert: time.Unix(int64(lastAdvert), 0), Lat: float64(lat) / 1e6, Lon: float64(lon) / 1e6}
	if pathLen != 0xFF {
		size, hops := meshcore.PathLenFields(pathLen)
		n := min(int(size)*int(hops), len(path))
		c.PathLen, c.Path, c.HashSize = int(hops), append([]byte(nil), path[:n]...), int(size)
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

func (l *Link) deliver(ms []client.WaitingMessage, queued bool) {
	now := time.Now()
	for _, w := range ms {
		in := Incoming{At: now, SNR: w.SNR, HasSNR: w.HasSNR, Queued: queued}
		switch {
		case w.Contact != nil:
			m := w.Contact
			in.From, in.Hops, in.Text = m.PubKeyPrefix, hopsFrom(m.PathLen), untrusted.Text(m.Text)
			in.SentAt, in.Signed = time.Unix(int64(m.SenderTimestamp), 0), m.TxtType == companion.TxtTypeSignedPlain
			in.CLI = m.TxtType == companion.TxtTypeCLIData
			if in.Signed && len(m.SenderPrefix) >= 4 {
				copy(in.Author[:], m.SenderPrefix[:4])
			}
		case w.Channel != nil:
			m := w.Channel
			in.Channel, in.ChannelIdx, in.Hops, in.Text = true, int(m.ChannelIdx), hopsFrom(m.PathLen), untrusted.Text(m.Text)
			in.SentAt = time.Unix(int64(m.SenderTimestamp), 0)
		case w.ChannelData != nil:
			d := w.ChannelData
			l.emit(ChannelData{At: now, Idx: int(d.ChannelIdx), SNR: d.SNR, Type: d.DataType, Data: d.Data})
			continue
		default:
			continue
		}
		l.emit(in)
	}
}

// nudge asks fetchLoop to fetch; several pushes while it's busy make one more fetch, not a pile of them.
func (l *Link) nudge() {
	select {
	case l.waiting <- struct{}{}:
	default:
	}
}

func (l *Link) fetchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.waiting:
		}
		for try := 1; ; try++ {
			err := l.fetch(ctx)
			if err == nil || ctx.Err() != nil {
				break
			}
			if try == fetchTries {
				l.emit(Log{At: time.Now(), Text: "COULDN'T FETCH MESSAGES  " + Explain(err) + "; they wait on the radio until the next try", Tone: ToneWarn})
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(fetchRetry):
			}
		}
	}
}

func (l *Link) fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	ms, err := l.client.GetWaitingMessages(ctx)
	l.deliver(ms, false)
	return err
}

// Explain words an error for the log, turning Go's timeout message into something plain.
func Explain(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "the radio didn't answer in time"
	}
	return err.Error()
}

// late catches a message whose fetch gave up waiting just before the radio answered. The radio has
// already taken it off its queue, so this is its only copy.
func late(r companion.Response) (client.WaitingMessage, bool) {
	switch d := r.Data.(type) {
	case companion.ContactMsgRecvResponse:
		return client.WaitingMessage{Contact: &d}, true
	case companion.ContactMsgRecvV3Response:
		m := companion.ContactMsgRecvResponse{PubKeyPrefix: d.PubKeyPrefix, PathLen: d.PathLen, TxtType: d.TxtType,
			SenderTimestamp: d.SenderTimestamp, SenderPrefix: d.SenderPrefix, Text: d.Text}
		return client.WaitingMessage{Contact: &m, SNR: d.SNR, HasSNR: true}, true
	case companion.ChannelMsgRecvResponse:
		return client.WaitingMessage{IsChannel: true, Channel: &d}, true
	case companion.ChannelMsgRecvV3Response:
		m := companion.ChannelMsgRecvResponse{ChannelIdx: d.ChannelIdx, PathLen: d.PathLen, TxtType: d.TxtType,
			SenderTimestamp: d.SenderTimestamp, SenderPrefix: d.SenderPrefix, Text: d.Text}
		return client.WaitingMessage{IsChannel: true, Channel: &m, SNR: d.SNR, HasSNR: true}, true
	case companion.ChannelDataRecvResponse:
		return client.WaitingMessage{IsChannel: true, ChannelData: &d, SNR: d.SNR, HasSNR: true}, true
	}
	return client.WaitingMessage{}, false
}

// maxRefresh bounds the contacts waiting to be re-read after a push; past it pushes are ignored.
const maxRefresh = 256

// queueRefresh asks the refresher to re-read a contact; push handlers run on the SDK's dispatch
// goroutine, so the command has to be sent from elsewhere.
func (l *Link) queueRefresh(key [32]byte) {
	l.refreshMu.Lock()
	if !l.refreshing[key] && len(l.refreshQ) < maxRefresh {
		l.refreshing[key] = true
		l.refreshQ = append(l.refreshQ, key)
	}
	l.refreshMu.Unlock()
	select {
	case l.refreshNow <- struct{}{}:
	default:
	}
}

// refresher re-reads one contact at a time, so a flood of pushes can't queue commands without limit.
func (l *Link) refresher(ctx context.Context) {
	for {
		l.refreshMu.Lock()
		if len(l.refreshQ) == 0 {
			l.refreshMu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-l.refreshNow:
			}
			continue
		}
		key := l.refreshQ[0]
		l.refreshQ = l.refreshQ[1:]
		delete(l.refreshing, key)
		l.refreshMu.Unlock()
		l.refreshContact(ctx, key)
	}
}

func (l *Link) refreshContact(ctx context.Context, key [32]byte) {
	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
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

// SendChannelAt sends a channel message and reports its timestamp. The SDK stamps the message with the time
// itself, so this waits until a second is well under way: the stamp is then this second's.
func (l *Link) SendChannelAt(ctx context.Context, idx int, text string) (uint32, error) {
	if ns := time.Now().Nanosecond(); ns > 800_000_000 {
		time.Sleep(time.Duration(1_000_000_000-ns) + 10*time.Millisecond)
	}
	ts := uint32(time.Now().Unix())
	_, err := l.client.SendChannelTextMessage(ctx, byte(idx), text, companion.TxtTypePlain)
	return ts, err
}

// signChunk is how much is fed to the radio to sign per frame, inside the 172-byte frame limit.
const signChunk = 160

// Sign has the radio sign data with its own key.
func (l *Link) Sign(ctx context.Context, data []byte) ([64]byte, error) {
	start, err := l.client.SignStart(ctx)
	if err != nil {
		return [64]byte{}, err
	}
	if uint32(len(data)) > start.MaxSignDataLen {
		return [64]byte{}, fmt.Errorf("%d bytes is more than the radio will sign (%d)", len(data), start.MaxSignDataLen)
	}
	for len(data) > 0 {
		n := min(len(data), signChunk)
		if err := l.client.SignData(ctx, data[:n]); err != nil {
			return [64]byte{}, err
		}
		data = data[n:]
	}
	sig, err := l.client.SignFinish(ctx)
	return sig.Signature, err
}

func (l *Link) SendChannelData(ctx context.Context, idx int, dataType uint16, data []byte) error {
	return l.client.SendChannelDataFlood(ctx, byte(idx), dataType, data)
}

func (l *Link) ResetPath(ctx context.Context, contact [32]byte) error {
	return l.client.ResetPath(ctx, meshcore.NewIdentity(contact))
}

// pollBattery doubles as a watchdog: a radio that stops answering is redialled, since a USB link
// can go quiet without reporting an error, after the computer sleeps for instance.
func (l *Link) pollBattery(ctx context.Context) {
	t := time.NewTicker(batteryEvery)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sctx, cancel := context.WithTimeout(ctx, pollTimeout)
			b, err := l.client.GetBattAndStorage(sctx)
			cancel()
			switch {
			case err == nil:
				misses = 0
				l.emit(Battery{b.BatteryMilliVolts})
			case errors.Is(err, context.DeadlineExceeded):
				if misses++; misses == silentPolls {
					misses = 0
					if r, ok := l.raw.(interface{ Reset() }); ok {
						l.emit(Log{At: time.Now(), Text: "NO ANSWER  the radio has stopped answering; reconnecting", Tone: ToneWarn})
						r.Reset()
					}
				}
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
			l.emit(Log{At: time.Now(), Text: fmt.Sprintf("HEARD  %s [%s], not a contact", untrusted.Line(p.AdvertName), hexPrefix(p.PublicKey[:1]))})
		}
	})
	c.OnPush(companion.PushAdvert, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushAdvertResponse); ok {
			l.emit(Log{At: time.Now(), Text: fmt.Sprintf("ADVERT  %s", hexPrefix(p.PublicKey[:6]))})
			l.queueRefresh(p.PublicKey)
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
			l.emit(TraceResult{Tag: p.Tag, Hashes: p.PathHashes, HashSize: 1 << (p.Flags & 3), SNRs: snrs, LastSNR: p.LastSNR})
		}
	})
	c.OnPush(companion.PushPathDiscoveryResponse, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushPathDiscoveryResp); ok {
			out, _ := meshcore.PathLenFields(p.OutPathLen)
			in, _ := meshcore.PathLenFields(p.InPathLen)
			l.emit(PathFound{Prefix: p.PubKeyPrefix, Out: p.OutPath, In: p.InPath, OutSize: int(out), InSize: int(in)})
		}
	})
	c.OnPush(companion.PushPathUpdated, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushPathUpdatedResponse); ok {
			l.emit(Log{At: time.Now(), Text: fmt.Sprintf("PATH UPDATED  %s", hexPrefix(p.PublicKey[:6]))})
			l.queueRefresh(p.PublicKey)
		}
	})
	c.OnPush(companion.PushSendConfirmed, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushSendConfirmedResponse); ok {
			l.emit(Acked{Ack: p.AckCode, RTT: time.Duration(p.RoundTrip) * time.Millisecond})
		}
	})
	c.OnPush(companion.PushMsgWaiting, func(companion.Response) { l.nudge() })
	for _, code := range []byte{companion.RespContactMsgRecv, companion.RespContactMsgRecvV3, companion.RespChannelMsgRecv, companion.RespChannelMsgRecvV3, companion.RespChannelDataRecv} {
		c.OnPush(code, func(r companion.Response) {
			if w, ok := late(r); ok {
				l.deliver([]client.WaitingMessage{w}, false)
				l.nudge() // the fetch that lost it stopped early, so more may be waiting
			}
		})
	}
	c.OnPush(companion.PushContactsFull, func(companion.Response) { l.emit(ContactsFull{}) })
	c.OnPush(companion.PushLoginSuccess, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushLoginSuccessResponse); ok {
			// Newer servers report an access level; older ones only whether you're admin.
			admin := p.Permissions&0x01 != 0
			level := byte(0)
			if admin {
				level = 3
			}
			if p.HasServerInfo && p.ACL != 0 {
				level = p.ACL & 0x03
				admin = level == 3
			}
			l.emit(LoginResult{Prefix: p.PubKeyPrefix, OK: true, Admin: admin, Permissions: level})
		}
	})
	c.OnPush(companion.PushLoginFail, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushLoginFailResponse); ok {
			l.emit(LoginResult{Prefix: p.PubKeyPrefix})
		}
	})
	c.OnPush(companion.PushStatusResponse, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushStatusResp); ok {
			l.emit(StatusReport{Prefix: p.PubKeyPrefix, Data: p.StatusData})
		}
	})
	c.OnPush(companion.PushTelemetryResponse, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushTelemetryResp); ok {
			l.emit(Telemetry{Prefix: p.PubKeyPrefix, LPP: p.LPPData})
		}
	})
	c.OnPush(companion.PushBinaryResponse, func(r companion.Response) {
		if p, ok := r.Data.(companion.PushBinaryResp); ok {
			l.emit(BinaryReply{Tag: p.Tag, Data: p.ResponseData})
		}
	})
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
		t.emit(CommandFrame(now, cmd))
		if overAir[cmd[0]] {
			t.emit(Transmit{At: now, What: CommandName(cmd[0])})
		}
	}
	return t.inner.Send(cmd)
}

func (t *tap) SetResponseHandler(h func(companion.Response)) {
	if s, ok := t.inner.(interface {
		SetFrameHandler(func(companion.Response, []byte))
	}); ok {
		s.SetFrameHandler(func(r companion.Response, raw []byte) {
			t.emit(ResponseFrame(time.Now(), raw))
			h(r)
		})
		return
	}
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
		size := pathHashSize(c)
		cmd.OutPathLen, cmd.OutPath = meshcore.MakePathLen(uint8(size), uint8(c.PathLen)), c.Path
	}
	return l.client.AddUpdateContactFull(ctx, cmd)
}

// pathHashSize trusts HashSize unless the path was edited to a different shape since it was read.
func pathHashSize(c Contact) int {
	size := max(c.HashSize, 1)
	if c.PathLen > 0 && len(c.Path) != size*c.PathLen && len(c.Path)%c.PathLen == 0 {
		size = len(c.Path) / c.PathLen
	}
	return min(size, 4)
}

func (l *Link) RemoveContact(ctx context.Context, key [32]byte) error {
	return l.client.RemoveContact(ctx, meshcore.NewIdentity(key))
}

// Trace sends a trace along path; the tag matches the TraceResult that comes back. The trace's flags carry
// the hop size as a power of two, so it's 1, 2 or 4 bytes.
func (l *Link) Trace(ctx context.Context, path []byte, hashSize int) (uint32, error) {
	flag, ok := map[int]byte{1: 0, 2: 1, 4: 2}[hashSize]
	if !ok || len(path)%hashSize != 0 {
		return 0, fmt.Errorf("a trace can't carry %d-byte hops", hashSize)
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	tag := binary.LittleEndian.Uint32(b[:])
	_, err := l.client.SendTracePath(ctx, tag, 0, flag, path)
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
	size, _ := meshcore.PathLenFields(r.PathLen)
	return AdvertPath{At: time.Unix(int64(r.RecvTimestamp), 0), Path: r.Path, HashSize: int(size)}, nil
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

func (l *Link) Login(ctx context.Context, key [32]byte, password string) error {
	_, err := l.client.SendLogin(ctx, meshcore.NewIdentity(key), password)
	return err
}

func (l *Link) Logout(ctx context.Context, key [32]byte) error {
	return l.client.Logout(ctx, meshcore.NewIdentity(key))
}

func (l *Link) RequestStatus(ctx context.Context, key [32]byte) error {
	_, err := l.client.SendStatusReq(ctx, meshcore.NewIdentity(key))
	return err
}

func (l *Link) RequestTelemetry(ctx context.Context, key [32]byte) error {
	_, err := l.client.SendTelemetryReq(ctx, meshcore.NewIdentity(key))
	return err
}

// BinaryRequest sends a typed request; the reply comes back carrying the tag from the SENT frame.
func (l *Link) BinaryRequest(ctx context.Context, key [32]byte, data []byte) (uint32, error) {
	r, err := l.client.SendBinaryReq(ctx, meshcore.NewIdentity(key), data)
	if err != nil {
		return 0, err
	}
	if r.HasExtended {
		return r.Tag, nil
	}
	return r.AckCode, nil
}

func (l *Link) SendCLI(ctx context.Context, key [32]byte, command string) error {
	_, err := l.client.SendTextMessage(ctx, meshcore.NewIdentity(key), command, companion.TxtTypeCLIData)
	return err
}

// Channels reads every slot; empty ones are left out.
func (l *Link) Channels(ctx context.Context) ([]Channel, error) {
	var out []Channel
	for i := 0; i < max(int(l.slots.Load()), 1); i++ {
		ch, err := l.client.GetChannel(ctx, byte(i))
		if err != nil {
			break
		}
		if ch.Name != "" {
			out = append(out, Channel{Idx: int(ch.ChannelIdx), Name: untrusted.Line(ch.Name), Secret: ch.Secret})
		}
	}
	return out, nil
}

func (l *Link) SetChannel(ctx context.Context, c Channel) error {
	return l.client.SetChannel(ctx, byte(c.Idx), c.Name, c.Secret)
}

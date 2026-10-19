// Package sim is a pretend companion radio on a small Bristol mesh, for --demo and tests.
package sim

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
)

type node struct {
	name string
	hash byte
	typ  radio.ContactType
	lat  float64
	lon  float64
}

var repeaters = []node{
	{"Dundry", 0xA3, radio.TypeRepeater, 51.399, -2.639}, {"Cabot Tower", 0x1E, radio.TypeRepeater, 51.452, -2.603},
	{"Purdown", 0x77, radio.TypeRepeater, 51.484, -2.564}, {"Kelston", 0x5F, radio.TypeRepeater, 51.399, -2.431},
	{"Lansdown", 0x5F, radio.TypeRepeater, 51.420, -2.410}, {"Blackdown", 0xB2, radio.TypeRepeater, 51.303, -2.715},
	{"Clevedon", 0x4D, radio.TypeRepeater, 51.440, -2.850}, {"Aust", 0x90, radio.TypeRepeater, 51.600, -2.620},
	{"Brean Down", 0xE1, radio.TypeRepeater, 51.324, -3.025}, {"Twmbarlwm", 0x08, radio.TypeRepeater, 51.623, -3.088},
}

var people = []node{
	{"Alice", 0x3A, radio.TypeChat, 51.346, -2.970}, {"Bob", 0xB0, radio.TypeChat, 51.381, -2.360},
	{"Carol", 0xCA, radio.TypeChat, 51.484, -2.766}, {"Dan", 0xD4, radio.TypeChat, 51.588, -2.997},
	{"Clifton Room", 0x6C, radio.TypeRoom, 51.456, -2.620},
}

// strangers are heard on air but aren't contacts until you add them.
var strangers = []node{
	{"Eve", 0xE5, radio.TypeChat, 51.410, -2.540}, {"Frank", 0xF4, radio.TypeChat, 51.330, -2.810},
	{"Weston Relay", 0x3B, radio.TypeRepeater, 51.350, -2.980}, {"Portishead Room", 0x9C, radio.TypeRoom, 51.486, -2.770},
}

var channelNames = []string{"Public", "#bristol", "#hilltop"}

var channels = simChannels()

// device is what the simulated radio holds; boot details are read from it rather than scripted.
var device = struct {
	self  radio.SelfInfo
	info  radio.DeviceInfo
	drift time.Duration
}{
	self:  radio.SelfInfo{Name: "MeshCore-k4", PubKey: key(0xC0, "MeshCore-k4"), FreqKHz: 869618, BwHz: 62500, SF: 8, CR: 8, TxPower: 22, Lat: 51.44, Lon: -2.598},
	info:  radio.DeviceInfo{Model: "Heltec V4", Firmware: "v1.17.1", BuildDate: "14 Aug 2026", MaxContacts: 350, MaxChannels: 40, PinSet: true},
	drift: 14 * time.Second,
}

func contacts() []radio.Contact {
	var out []radio.Contact
	for i, n := range append(append([]node(nil), repeaters...), people...) {
		c := radio.Contact{Key: key(n.hash, n.name), Name: n.name, Type: n.typ, PathLen: -1, Lat: n.lat, Lon: n.lon,
			LastAdvert: time.Now().Add(-time.Duration(i*7) * time.Minute)}
		if n.typ != radio.TypeRepeater {
			c.PathLen, c.Path = 2, []byte{0xA3, 0x1E}
		}
		out = append(out, c)
	}
	return out
}

var chatter = map[string][]string{
	"#bristol": {
		"@[MeshCore-k4] are you hearing Purdown from Bedminster?", "@[MeshCore-k4] nice advert, came in at 3 hops",
		"anyone hearing the coast relay this morning?", "Purdown's back up after the power cut",
		"new repeater going up on Lansdown this weekend", "getting 3 hops to Bath from Bedminster now",
		"is the #hilltop channel still a thing?", "solid -2 dB to Clevedon from the harbourside",
	},
	"Public":   {"hello from Weston", "test test, 1 2", "first packet from a new Heltec V4!"},
	"#hilltop": {"Dundry repeater battery looks low, 3.6 V", "swapped the antenna on Blackdown, much better"},
}

var dms = map[string][]string{
	"Alice": {"are you on the mesh tonight?", "the trace to Brean Down worked", "nice, that's a clean route"},
	"Bob":   {"got your message via Kelston", "battery's at 80% here", "back in 10"},
	"Carol": {"Portishead is coming through loud and clear", "can you see my advert?"},
	"Dan":   {"hello from across the estuary!", "Twmbarlwm to Aust is a long hop"},
}

// Sim implements radio.Radio with timings close to a real companion over USB.
type Sim struct {
	events chan radio.Event
	speed  float64 // 1 is real time; tests run faster
	cancel context.CancelFunc
	once   sync.Once

	mu       sync.Mutex
	rng      *rand.Rand
	mv       uint16
	ack      uint32
	pending  map[string]int // replies each person owes, after you message them
	loss     float64        // chance an ack never arrives
	book     []radio.Contact
	quiet    bool
	servers  map[[32]byte]*server
	sessions map[[32]byte]byte // logged-in servers and the level granted
	posts    []post
	born     time.Time
	auto     radio.AutoAdd
	tag      uint32
	dev      devSettings
	chans    []radio.Channel
}

func New(seed int64) *Sim {
	return &Sim{events: make(chan radio.Event, 256), rng: rand.New(rand.NewSource(seed)), speed: 1, mv: 4020,
		ack: uint32(seed), pending: map[string]int{}, loss: 0.15, book: contacts(),
		auto: radio.AutoAdd{Repeater: true, MaxHops: 0}, servers: map[[32]byte]*server{}, sessions: map[[32]byte]byte{},
		posts: seededPosts(time.Now()), born: time.Now(), dev: newDevSettings(),
		chans: append([]radio.Channel(nil), channels...)}
}

// Fast makes every delay negligible, for tests.
func (s *Sim) Fast() *Sim { s.speed = 1000; return s }

// Lossless makes every ack arrive, for tests that need a predictable outcome.
func (s *Sim) Lossless() *Sim { s.loss = 0; return s }

// Quiet stops the background traffic, so tests see only the answers to what they ask.
func (s *Sim) Quiet() *Sim { s.quiet = true; return s }

func (s *Sim) Events() <-chan radio.Event { return s.events }
func (s *Sim) Describe() string           { return "simulated radio" }
func (s *Sim) Transport() string          { return "SIM" }

func (s *Sim) Close() error {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	return nil
}

func (s *Sim) intn(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Intn(n)
}

func (s *Sim) float() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Float64()
}

func (s *Sim) delay(ms int) time.Duration {
	return time.Duration(float64(ms)/s.speed) * time.Millisecond
}

func (s *Sim) sleep(ctx context.Context, ms int) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(s.delay(ms)):
		return true
	}
}

func (s *Sim) emit(e radio.Event) {
	select {
	case s.events <- e:
	default:
	}
}

func (s *Sim) frame(cmd byte, resp byte, args ...byte) {
	now := time.Now()
	s.emit(radio.Frame{At: now, Dir: radio.ToRadio, Code: cmd, Name: radio.CommandName(cmd), Bytes: append([]byte{cmd}, args...)})
	s.emit(radio.Frame{At: now.Add(8 * time.Millisecond), Dir: radio.FromRadio, Code: resp, Name: radio.ResponseName(resp)})
}

func (s *Sim) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.run(ctx)
}

func prefix(k [32]byte) [6]byte {
	var p [6]byte
	copy(p[:], k[:6])
	return p
}

// queued is what was waiting on the radio while we were away.
func queued(now time.Time) []radio.Incoming {
	return []radio.Incoming{
		{At: now, Channel: true, ChannelIdx: 1, Hops: 3, SentAt: now.Add(-42 * time.Minute), Text: "Carol: morning all, Purdown is back", SNR: 6.5, HasSNR: true},
		{At: now, Channel: true, ChannelIdx: 1, Hops: 2, SentAt: now.Add(-38 * time.Minute), Text: "Bob: 3 hops to Bath now", SNR: 2.25, HasSNR: true},
		{At: now, From: prefix(key(0x3A, "Alice")), Hops: -1, SentAt: now.Add(-20 * time.Minute), Text: "ping me when you're back on", SNR: 9.5, HasSNR: true},
	}
}

func (s *Sim) run(ctx context.Context) {
	clock := "in sync"
	if device.drift.Abs() > 5*time.Second {
		clock = fmt.Sprintf("synced, was %+v off", device.drift)
	}
	cs := s.SnapshotContacts()
	waiting := queued(time.Now())
	batt := radio.Battery{MilliVolts: s.mv}
	steps := []struct {
		key, label, detail string
		cmd, resp          byte
		ms                 int
	}{
		{"link", "LINK", s.Describe(), 0, 0, 260},
		{"hello", "HELLO", device.self.Name, 1, 5, 140},
		{"device", "DEVICE", device.info.Model + " · fw " + device.info.Firmware, 22, 13, 120},
		{"clock", "CLOCK", clock, 5, 9, 160},
		{"contacts", "CONTACTS", fmt.Sprintf("%d synced", len(cs)), 4, 4, 40 * len(cs)},
		{"channels", "CHANNELS", fmt.Sprintf("%d configured", len(s.channelList())), 31, 18, 100 * len(s.channelList())},
		{"queue", "QUEUE", fmt.Sprintf("%d received", len(waiting)), 10, 10, 90 * len(waiting)},
		{"power", "POWER", fmt.Sprintf("%.2f V · %d%%", float64(batt.MilliVolts)/1000, batt.Percent()), 20, 12, 120},
	}
	for _, st := range steps {
		s.emit(radio.BootStep{Key: st.key, Label: st.label, State: radio.StepWait})
		if !s.sleep(ctx, st.ms) {
			return
		}
		if st.cmd != 0 {
			s.frame(st.cmd, st.resp)
		}
		s.emit(radio.BootStep{Key: st.key, Label: st.label, Detail: st.detail, State: radio.StepOK})
	}
	info := device.info
	s.mu.Lock()
	info.PathHash = s.dev.s.PathHashMode + 1
	s.mu.Unlock()
	s.emit(radio.Ready{Self: device.self, Device: info, Contacts: len(cs), Channels: len(s.channelList()), Battery: batt,
		ContactList: cs, ChannelList: s.channelList()})
	for _, m := range waiting {
		s.emit(m)
	}
	if !s.quiet {
		s.traffic(ctx)
	}
}

// traffic emits a believable stream of heard packets, messages, adverts and the odd transmission.
func (s *Sim) traffic(ctx context.Context) {
	lastBatt := time.Now()
	for {
		if !s.sleep(ctx, 700+s.intn(2600)) {
			return
		}
		now := time.Now()
		switch x := s.float(); {
		case x < 0.62:
			s.hear(now)
		case x < 0.76:
			s.chatter(now)
		case x < 0.80:
			s.stranger(now)
		case x < 0.90:
			s.frame(7, 0, 1)
			s.emit(radio.Transmit{At: now, What: "SEND_SELF_ADVERT"})
			s.emit(radio.Log{At: now, Text: "TX  FLOOD ADVERT  " + device.self.Name, Tone: radio.ToneInfo})
		default:
			r := repeaters[s.intn(len(repeaters))]
			p := people[s.intn(len(people))]
			s.emit(radio.Log{At: now, Text: fmt.Sprintf("PATH UPDATED  %s → %02X", p.name, r.hash), Tone: radio.ToneOK})
		}
		if now.Sub(lastBatt) > s.delay(20000) {
			lastBatt = now
			s.mu.Lock()
			s.mv -= uint16(s.rng.Intn(4))
			mv := s.mv
			s.mu.Unlock()
			s.frame(20, 12)
			s.emit(radio.Battery{MilliVolts: mv})
		}
	}
}

func (s *Sim) hear(now time.Time) {
	raw, desc, flood := s.packet(now)
	s.heardPacket(now, raw)
	if desc != "" {
		s.emit(radio.Log{At: now, Text: desc})
	}
	// A flood often reaches us twice, by different repeaters.
	if flood && s.float() < 0.3 {
		if again := s.reroute(raw); again != nil {
			go func() {
				time.Sleep(s.delay(150 + s.intn(400)))
				s.heardPacket(time.Now(), again)
			}()
		}
	}
}

func (s *Sim) heardPacket(at time.Time, raw []byte) {
	s.emit(radio.Frame{At: at, Dir: radio.FromRadio, Code: 0x88, Name: radio.ResponseName(0x88)})
	s.emit(radio.Packet{At: at, SNR: float32(s.intn(64)-20) / 4, RSSI: int8(-70 - s.intn(50)), Raw: raw})
}

// reroute is the same packet with a different path, as if heard via other repeaters.
func (s *Sim) reroute(raw []byte) []byte {
	p, err := meshcore.PacketFromBytes(raw)
	if err != nil {
		return nil
	}
	hops := s.hops(1 + s.intn(4))
	p.Path, p.PathLength = slices.Concat(hops...), meshcore.MakePathLen(uint8(len(hops[0])), uint8(len(hops)))
	out, err := p.ToBytes()
	if err != nil {
		return nil
	}
	return out
}

// hops is a made-up path through n repeaters, each hop as long as the device's path hash size.
func (s *Sim) hops(n int) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := make([][]byte, n)
	for i := range path {
		r := repeaters[s.rng.Intn(len(repeaters))]
		path[i] = s.hopOf(r)
	}
	return path
}

// hopOf is a repeater's hash in the paths this radio starts; call with s.mu held.
func (s *Sim) hopOf(r node) []byte {
	k := key(r.hash, r.name)
	return k[:s.dev.s.PathHashMode+1]
}

func (s *Sim) bytes(n int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := make([]byte, n)
	s.rng.Read(b)
	return b
}

func build(route, payloadType byte, hops [][]byte, payload []byte) []byte {
	size := 1
	if len(hops) > 0 {
		size = len(hops[0])
	}
	p := &meshcore.Packet{Header: meshcore.MakeHeader(route, payloadType, 0), PathLength: meshcore.MakePathLen(uint8(size), uint8(len(hops))),
		Path: slices.Concat(hops...), Payload: payload}
	raw, err := p.ToBytes()
	if err != nil {
		panic(err) // the simulator only builds valid packets
	}
	return raw
}

var advertTypes = map[radio.ContactType]string{radio.TypeChat: "CHAT", radio.TypeRepeater: "REPEATER", radio.TypeRoom: "ROOM", radio.TypeSensor: "SENSOR"}

// packet builds a real on-air packet. Adverts are signed and channel messages are encrypted with the
// channel's secret, so they decode as they would off a real radio; private traffic is the right shape
// with random ciphertext.
func (s *Sim) packet(now time.Time) ([]byte, string, bool) {
	all := append(append(append([]node(nil), repeaters...), people...), strangers...)
	hops := s.hops(1 + s.intn(4))
	encrypted := func() []byte {
		b := s.bytes(4 + 16*(1+s.intn(3)))
		b[0], b[1] = all[s.intn(len(all))].hash, device.self.PubKey[0] // destination and source hashes, then MAC and ciphertext
		return b
	}
	switch x := s.intn(100); {
	case x < 26:
		n := all[s.intn(len(all))]
		adv, err := meshcore.NewAdvert(ident(n.hash, n.name), meshcore.AdvertAppData{Type: advertTypes[n.typ], Name: n.name,
			Lat: int32(n.lat * 1e6), Lon: int32(n.lon * 1e6), HasLocation: true}, uint32(now.Unix()))
		if err != nil {
			return nil, "", false
		}
		payload, _ := adv.ToBytes()
		desc := ""
		if s.intn(3) == 0 {
			desc = fmt.Sprintf("ADVERT  %s [%02X]  %d hops", n.name, n.hash, len(hops))
		}
		return build(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, hops, payload), desc, true
	case x < 52:
		cs := s.channelList()
		if len(cs) == 0 {
			return nil, "", false
		}
		return s.groupText(now, hops, cs[s.intn(len(cs))], []string{"Alice", "Bob", "Carol", "Eve"}[s.intn(4)], ""), "", true
	case x < 64:
		return build(meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, hops, encrypted()), "", false
	case x < 78:
		return build(meshcore.RouteTypeDirect, meshcore.PayloadTypeAck, hops, s.bytes(4)), "", false
	case x < 84:
		return build(meshcore.RouteTypeFlood, meshcore.PayloadTypePath, hops, encrypted()), "", true
	case x < 90:
		return build(meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, hops, encrypted()), "", false
	case x < 96:
		return build(meshcore.RouteTypeDirect, meshcore.PayloadTypeResponse, hops, encrypted()), "", false
	default:
		tr := meshcore.Trace{Tag: binary.LittleEndian.Uint32(s.bytes(4))}
		for _, h := range s.hops(1 + s.intn(3)) {
			if len(h) == 3 {
				h = h[:2] // a trace's hops are a power of two long
			}
			tr.Flags = byte(len(h) / 2)
			tr.PathHashes = append(tr.PathHashes, h...)
		}
		payload, _ := tr.ToBytes()
		return build(meshcore.RouteTypeDirect, meshcore.PayloadTypeTrace, hops, payload), "", false
	}
}

// groupText encrypts a channel message the way the sender's radio would.
func (s *Sim) groupText(now time.Time, hops [][]byte, ch radio.Channel, sender, text string) []byte {
	if text == "" {
		lines := chatter[ch.Name]
		text = lines[s.intn(len(lines))]
	}
	psk := ch.Secret
	gt, err := (&meshcore.GroupTextPayload{Timestamp: uint32(now.Unix()), Sender: sender, Text: text}).Encrypt(meshcore.DeriveChannelHash(psk), psk[:])
	if err != nil {
		return nil
	}
	payload, _ := gt.ToBytes()
	return build(meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, hops, payload)
}

// chatter is a channel message from someone on the mesh, or a reply someone owes you.
func (s *Sim) chatter(now time.Time) {
	s.mu.Lock()
	var owed string
	for name, n := range s.pending {
		if n > 0 {
			owed = name
			s.pending[name]--
			break
		}
	}
	s.mu.Unlock()
	if owed != "" || s.float() < 0.45 {
		name := owed
		if name == "" {
			name = []string{"Alice", "Bob", "Carol", "Dan"}[s.intn(4)]
		}
		lines := dms[name]
		s.emit(radio.Incoming{At: now, From: prefix(key(peopleHash(name), name)), Hops: s.intn(3) + 1, SentAt: now,
			Text: lines[s.intn(len(lines))], SNR: float32(s.intn(48)-12) / 4, HasSNR: true})
		return
	}
	cs := s.channelList()
	if len(cs) == 0 {
		return
	}
	ch := cs[s.intn(len(cs))]
	lines, ok := chatter[ch.Name]
	if !ok {
		lines = []string{"hello from " + ch.Name, "anyone else on here?", "testing the new channel"}
	}
	who := []string{"Alice", "Bob", "Carol", "Dan", "Eve", "Hilltop Ops"}[s.intn(6)]
	text := lines[s.intn(len(lines))]
	hops := s.hops(s.intn(4) + 1)
	// The radio hears the encrypted packet and then hands over the message it decrypted.
	s.heardPacket(now, s.groupText(now, hops, ch, who, text))
	sent := time.Unix(now.Unix(), 0)
	s.emit(radio.Incoming{At: now, Channel: true, ChannelIdx: ch.Idx, Hops: len(hops), SentAt: sent,
		Text: who + ": " + text, SNR: float32(s.intn(48)-12) / 4, HasSNR: true})
	if who != "Alice" {
		return
	}
	// Alice signs most of her channel messages. Now and then someone else posts as Alice, signing with
	// their own key, or doesn't sign: what the signing checks are for.
	signer := ident(peopleHash("Alice"), "Alice")
	switch x := s.float(); {
	case x < 0.15:
		return
	case x < 0.25:
		signer = ident(0x99, "Mallory")
	}
	go func() {
		time.Sleep(s.delay(400))
		s.emit(radio.ChannelData{At: time.Now(), Idx: ch.Idx, SNR: 4, Type: signing.DataType, Data: signedDatagram(signer, ch.Name, uint32(sent.Unix()), text)})
	}()
}

// signedDatagram is the signature for a channel message, as SIGNING.md has a companion send it.
func signedDatagram(id meshcore.LocalIdentity, channel string, ts uint32, text string) []byte {
	d := signing.Datagram{TS: ts}
	pub := id.PublicKey()
	copy(d.KeyID[:], pub[:8])
	copy(d.Sig[:], id.Sign(signing.Canonical(channel, ts, text)))
	return d.Bytes()
}

func peopleHash(name string) byte {
	for _, p := range people {
		if p.name == name {
			return p.hash
		}
	}
	return 0
}

// person finds a chat or room contact by key, including ones added during the session.
func (s *Sim) person(to [32]byte) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.known(to); i >= 0 && s.book[i].Type != radio.TypeRepeater {
		return s.book[i].Name, true
	}
	return "", false
}

// SendText acks most messages after a believable round trip; some acks never arrive, so retries happen.
func (s *Sim) SendText(ctx context.Context, to [32]byte, text string) (radio.Sent, error) {
	if len(text) > radio.MaxText {
		return radio.Sent{}, errors.New("message too long")
	}
	name, ok := s.person(to)
	if !ok {
		return radio.Sent{}, errors.New("not a contact")
	}
	s.mu.Lock()
	s.ack++
	ack := s.ack
	lost := s.rng.Float64() < s.loss
	rtt := time.Duration(600+s.rng.Intn(1900)) * time.Millisecond
	if _, scripted := dms[name]; scripted && s.rng.Float64() < 0.5 {
		s.pending[name]++
	}
	s.mu.Unlock()

	s.frame(2, 6, []byte(text)...)
	s.emit(radio.Transmit{At: time.Now(), What: "SEND_TXT_MSG"})
	s.mu.Lock()
	isRoom := s.book[s.known(to)].Type == radio.TypeRoom
	s.mu.Unlock()
	if isRoom {
		s.postToRoom(text)
	}
	if !lost {
		go func() {
			time.Sleep(time.Duration(float64(rtt) / s.speed))
			s.emit(radio.Frame{At: time.Now(), Dir: radio.FromRadio, Code: 0x82, Name: radio.ResponseName(0x82)})
			s.emit(radio.Acked{Ack: ack, RTT: rtt})
		}()
	}
	return radio.Sent{Ack: ack, Timeout: time.Duration(float64(4*time.Second) / s.speed)}, nil
}

func (s *Sim) SendChannelAt(ctx context.Context, idx int, text string) (uint32, error) {
	ts := uint32(time.Now().Unix())
	return ts, s.SendChannel(ctx, idx, text)
}

// Sign signs as the radio does, with its own key.
func (s *Sim) Sign(ctx context.Context, data []byte) ([64]byte, error) {
	s.frame(33, 19)
	var sig [64]byte
	copy(sig[:], ident(0xC0, "MeshCore-k4").Sign(data))
	return sig, nil
}

func (s *Sim) SendChannelData(ctx context.Context, idx int, dataType uint16, data []byte) error {
	if !slices.ContainsFunc(s.channelList(), func(c radio.Channel) bool { return c.Idx == idx }) {
		return errors.New("no such channel")
	}
	s.frame(62, 0, data...)
	s.emit(radio.Transmit{At: time.Now(), What: "SEND_CHANNEL_DATA"})
	return nil
}

func (s *Sim) SendChannel(ctx context.Context, idx int, text string) error {
	if !slices.ContainsFunc(s.channelList(), func(c radio.Channel) bool { return c.Idx == idx }) {
		return errors.New("no such channel")
	}
	s.frame(3, 0, []byte(text)...)
	s.emit(radio.Transmit{At: time.Now(), What: "SEND_CHANNEL_TXT_MSG"})
	go s.echo(idx, text)
	if strings.Contains(strings.ToLower(text), "anyone") {
		go func() {
			time.Sleep(s.delay(2500))
			s.emit(radio.Incoming{At: time.Now(), Channel: true, ChannelIdx: idx, Hops: 2, SentAt: time.Now(), Text: "Carol: hearing you loud and clear", SNR: 5, HasSNR: true})
		}()
	}
	return nil
}

func (s *Sim) ResetPath(ctx context.Context, contact [32]byte) error {
	s.frame(13, 0)
	return nil
}

// SnapshotContacts is a copy of the simulated contact table.
func (s *Sim) SnapshotContacts() []radio.Contact {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]radio.Contact(nil), s.book...)
}

func asContact(n node) radio.Contact {
	return radio.Contact{Key: key(n.hash, n.name), Name: n.name, Type: n.typ, PathLen: -1, Lat: n.lat, Lon: n.lon, LastAdvert: time.Now()}
}

func (s *Sim) known(k [32]byte) int {
	for i, c := range s.book {
		if c.Key == k {
			return i
		}
	}
	return -1
}

// stranger is an advert from someone who isn't a contact; it's added only if auto-add covers its type.
func (s *Sim) stranger(now time.Time) {
	n := strangers[s.intn(len(strangers))]
	c := asContact(n)
	s.mu.Lock()
	if s.known(c.Key) >= 0 {
		s.mu.Unlock()
		return
	}
	add := n.typ == radio.TypeChat && s.auto.Chat || n.typ == radio.TypeRepeater && s.auto.Repeater ||
		n.typ == radio.TypeRoom && s.auto.Room || n.typ == radio.TypeSensor && s.auto.Sensor
	if add {
		s.book = append(s.book, c)
	}
	s.mu.Unlock()
	if add {
		s.emit(radio.ContactUpdated{Contact: c})
		s.emit(radio.Log{At: now, Text: fmt.Sprintf("NEW CONTACT  %s [%02X], added automatically", n.name, n.hash), Tone: radio.ToneOK})
		return
	}
	s.emit(radio.Heard{Contact: c})
	s.emit(radio.Log{At: now, Text: fmt.Sprintf("HEARD  %s [%02X], not a contact", n.name, n.hash)})
}

func (s *Sim) SyncContacts(ctx context.Context) ([]radio.Contact, error) {
	s.frame(4, 4)
	return s.SnapshotContacts(), nil
}

func (s *Sim) SaveContact(ctx context.Context, c radio.Contact) error {
	s.mu.Lock()
	if i := s.known(c.Key); i >= 0 {
		s.book[i] = c
	} else {
		s.book = append(s.book, c)
	}
	s.mu.Unlock()
	s.frame(9, 0)
	return nil
}

func (s *Sim) RemoveContact(ctx context.Context, k [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.known(k)
	if i < 0 {
		return errors.New("not found")
	}
	s.book = append(s.book[:i], s.book[i+1:]...)
	return nil
}

// linkSNR is a stable, believable signal for a hop between two hashes.
func linkSNR(a, b byte) float32 {
	v := int(a)*31 + int(b)*17
	return float32(v%56-14) / 4
}

// Trace answers after a delay that grows with the path. A hop through an unknown hash is lost, so no answer comes.
func (s *Sim) Trace(ctx context.Context, path []byte, hashSize int) (uint32, error) {
	if hashSize != 1 && hashSize != 2 && hashSize != 4 || len(path)%hashSize != 0 {
		return 0, fmt.Errorf("a trace can't carry %d-byte hops", hashSize)
	}
	s.mu.Lock()
	s.tag++
	tag := s.tag
	s.mu.Unlock()
	s.frame(36, 6, path...)
	s.emit(radio.Transmit{At: time.Now(), What: "SEND_TRACE_PATH"})
	hops := slices.Collect(slices.Chunk(path, hashSize))
	for _, h := range hops {
		ok := false
		for _, r := range append(append([]node(nil), repeaters...), strangers...) {
			k := key(r.hash, r.name)
			ok = ok || bytes.HasPrefix(k[:], h) && r.typ == radio.TypeRepeater
		}
		if !ok {
			return tag, nil
		}
	}
	go func() {
		time.Sleep(s.delay(400 + 300*len(hops)))
		snrs := make([]float32, len(hops))
		prev := device.self.PubKey[0]
		for i, h := range hops {
			snrs[i] = linkSNR(prev, h[0])
			prev = h[0]
		}
		s.emit(radio.Frame{At: time.Now(), Dir: radio.FromRadio, Code: 0x89, Name: radio.ResponseName(0x89)})
		s.emit(radio.TraceResult{Tag: tag, Hashes: append([]byte(nil), path...), HashSize: hashSize, SNRs: snrs, LastSNR: linkSNR(prev, device.self.PubKey[0])})
	}()
	return tag, nil
}

// DiscoverPath finds a route after a moment and stores it on the contact, as the firmware does.
func (s *Sim) DiscoverPath(ctx context.Context, k [32]byte) error {
	s.mu.Lock()
	i := s.known(k)
	if i < 0 {
		s.mu.Unlock()
		return errors.New("not a contact")
	}
	routes := [][]int{{0}, {0, 1}, {2, 3}, {1, 6}, {0, 5, 8}} // repeaters, by place in the list
	route := routes[s.rng.Intn(len(routes))]
	size := s.dev.s.PathHashMode + 1
	var out, in []byte
	for j := range route {
		out = append(out, s.hopOf(repeaters[route[j]])...)
		in = append(in, s.hopOf(repeaters[route[len(route)-1-j]])...)
	}
	s.book[i].PathLen, s.book[i].Path, s.book[i].HashSize = len(route), append([]byte(nil), out...), size
	s.mu.Unlock()
	s.frame(52, 6)
	s.emit(radio.Transmit{At: time.Now(), What: "SEND_PATH_DISCOVERY_REQ"})
	go func() {
		time.Sleep(s.delay(1500))
		s.emit(radio.Frame{At: time.Now(), Dir: radio.FromRadio, Code: 0x8D, Name: radio.ResponseName(0x8D)})
		s.emit(radio.PathFound{Prefix: prefix(k), Out: out, In: in, OutSize: size, InSize: size})
	}()
	return nil
}

func (s *Sim) AdvertPath(ctx context.Context, k [32]byte) (radio.AdvertPath, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.known(k)
	if i < 0 {
		return radio.AdvertPath{}, errors.New("not found")
	}
	paths := [][]byte{{}, {0xA3}, {0x1E, 0xA3}, {0x90, 0x77}, {0xE1, 0xB2, 0xA3}}
	return radio.AdvertPath{At: s.book[i].LastAdvert, Path: paths[int(k[1])%len(paths)]}, nil
}

// simLink marks links made by the simulator; real ones carry a signed advert it can't produce.
const simLink = 0x5A

func (s *Sim) ExportContact(ctx context.Context, k [32]byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.known(k)
	if i < 0 {
		return "", errors.New("not found")
	}
	c := s.book[i]
	data := append([]byte{simLink}, c.Key[:]...)
	data = append(data, byte(c.Type))
	data = append(data, []byte(c.Name)...)
	return radio.LinkPrefix + fmt.Sprintf("%x", data), nil
}

func (s *Sim) ImportContact(ctx context.Context, link string) error {
	data, err := radio.ParseLink(link)
	if err != nil {
		return err
	}
	if data[0] != simLink || len(data) < 35 {
		return errors.New("the simulator can only import links it made; real links need a real radio")
	}
	var k [32]byte
	copy(k[:], data[1:33])
	return s.SaveContact(ctx, radio.Contact{Key: k, Type: radio.ContactType(data[33]), Name: string(data[34:]), PathLen: -1, LastAdvert: time.Now()})
}

func (s *Sim) AutoAdd(ctx context.Context) (radio.AutoAdd, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auto, nil
}

func (s *Sim) SetAutoAdd(ctx context.Context, a radio.AutoAdd) error {
	s.mu.Lock()
	s.auto = a
	s.mu.Unlock()
	s.frame(58, 0)
	return nil
}

func (s *Sim) channelList() []radio.Channel {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]radio.Channel(nil), s.chans...)
}

func (s *Sim) Channels(ctx context.Context) ([]radio.Channel, error) {
	s.frame(31, 18)
	return s.channelList(), nil
}

// SetChannel writes a slot; an empty name frees it, as the firmware does.
func (s *Sim) SetChannel(ctx context.Context, c radio.Channel) error {
	if c.Idx < 0 || c.Idx >= device.info.MaxChannels {
		return errors.New("no such channel slot")
	}
	s.frame(32, 0, []byte(c.Name)...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chans = slices.DeleteFunc(s.chans, func(o radio.Channel) bool { return o.Idx == c.Idx })
	if c.Name != "" {
		s.chans = append(s.chans, c)
		slices.SortFunc(s.chans, func(a, b radio.Channel) int { return a.Idx - b.Idx })
	}
	return nil
}

// echo plays back our channel message as nearby repeaters pass it on; with loss, sometimes none do.
func (s *Sim) echo(idx int, text string) {
	s.mu.Lock()
	copies := 1 + s.rng.Intn(3)
	if s.rng.Float64() < s.loss/2 {
		copies = 0
	}
	s.mu.Unlock()
	var ch radio.Channel
	for _, c := range s.channelList() {
		if c.Idx == idx {
			ch = c
		}
	}
	for i := 0; i < copies; i++ {
		time.Sleep(s.delay(400 + s.intn(900)))
		now := time.Now()
		s.heardPacket(now, s.groupText(now, s.hops(i+1), ch, device.self.Name, text))
	}
}

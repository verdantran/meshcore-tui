// Package sim is a pretend companion radio on a small Bristol mesh, for --demo and tests.
package sim

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
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

var channels = []radio.Channel{{Idx: 0, Name: "Public"}, {Idx: 1, Name: "#bristol"}, {Idx: 2, Name: "#hilltop"}}

// device is what the simulated radio holds; boot details are read from it rather than scripted.
var device = struct {
	self  radio.SelfInfo
	info  radio.DeviceInfo
	drift time.Duration
}{
	self:  radio.SelfInfo{Name: "MeshCore-k4", PubKey: key(0xC0, "MeshCore-k4"), FreqKHz: 869618, BwHz: 62500, SF: 8, CR: 8, TxPower: 22, Lat: 51.44, Lon: -2.598},
	info:  radio.DeviceInfo{Model: "Heltec V4", Firmware: "v1.17.1", BuildDate: "14 Aug 2026", MaxContacts: 350, MaxChannels: 40},
	drift: 14 * time.Second,
}

// key makes a stable public key whose first byte is the node's path hash.
func key(hash byte, name string) [32]byte {
	k := sha256.Sum256([]byte(name))
	k[0] = hash
	return k
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

	mu      sync.Mutex
	rng     *rand.Rand
	mv      uint16
	ack     uint32
	pending map[string]int // replies each person owes, after you message them
	loss    float64        // chance an ack never arrives
}

func New(seed int64) *Sim {
	return &Sim{events: make(chan radio.Event, 256), rng: rand.New(rand.NewSource(seed)), speed: 1, mv: 4020,
		ack: uint32(seed), pending: map[string]int{}, loss: 0.15}
}

// Fast makes every delay negligible, for tests.
func (s *Sim) Fast() *Sim { s.speed = 1000; return s }

// Lossless makes every ack arrive, for tests that need a predictable outcome.
func (s *Sim) Lossless() *Sim { s.loss = 0; return s }

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
	cs := contacts()
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
		{"channels", "CHANNELS", fmt.Sprintf("%d configured", len(channels)), 31, 18, 100 * len(channels)},
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
	s.emit(radio.Ready{Self: device.self, Device: device.info, Contacts: len(cs), Channels: len(channels), Battery: batt,
		ContactList: cs, ChannelList: channels})
	for _, m := range waiting {
		s.emit(m)
	}
	s.traffic(ctx)
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
		case x < 0.80:
			s.chatter(now)
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
	raw, desc := s.packet()
	s.emit(radio.Frame{At: now, Dir: radio.FromRadio, Code: 0x88, Name: radio.ResponseName(0x88)})
	s.emit(radio.Packet{At: now, SNR: float32(s.intn(64)-20) / 4, RSSI: int8(-70 - s.intn(50)), Raw: raw})
	if desc != "" {
		s.emit(radio.Log{At: now, Text: desc})
	}
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
	if owed != "" || s.float() < 0.3 {
		name := owed
		if name == "" {
			name = []string{"Alice", "Bob", "Carol", "Dan"}[s.intn(4)]
		}
		lines := dms[name]
		s.emit(radio.Incoming{At: now, From: prefix(key(peopleHash(name), name)), Hops: s.intn(3) + 1, SentAt: now,
			Text: lines[s.intn(len(lines))], SNR: float32(s.intn(48)-12) / 4, HasSNR: true})
		return
	}
	ch := channels[s.intn(len(channels))]
	lines := chatter[ch.Name]
	who := []string{"Alice", "Bob", "Carol", "Dan", "Eve", "Hilltop Ops"}[s.intn(6)]
	s.emit(radio.Incoming{At: now, Channel: true, ChannelIdx: ch.Idx, Hops: s.intn(4) + 1, SentAt: now,
		Text: who + ": " + lines[s.intn(len(lines))], SNR: float32(s.intn(48)-12) / 4, HasSNR: true})
}

func peopleHash(name string) byte {
	for _, p := range people {
		if p.name == name {
			return p.hash
		}
	}
	return 0
}

func (s *Sim) person(to [32]byte) (string, bool) {
	for _, p := range people {
		if key(p.hash, p.name) == to {
			return p.name, true
		}
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
	if s.rng.Float64() < 0.5 {
		s.pending[name]++
	}
	s.mu.Unlock()

	s.frame(2, 6, []byte(text)...)
	s.emit(radio.Transmit{At: time.Now(), What: "SEND_TXT_MSG"})
	if !lost {
		go func() {
			time.Sleep(time.Duration(float64(rtt) / s.speed))
			s.emit(radio.Frame{At: time.Now(), Dir: radio.FromRadio, Code: 0x82, Name: radio.ResponseName(0x82)})
			s.emit(radio.Acked{Ack: ack, RTT: rtt})
		}()
	}
	return radio.Sent{Ack: ack, Timeout: time.Duration(float64(4*time.Second) / s.speed)}, nil
}

func (s *Sim) SendChannel(ctx context.Context, idx int, text string) error {
	if idx < 0 || idx >= len(channels) {
		return errors.New("no such channel")
	}
	s.frame(3, 0, []byte(text)...)
	s.emit(radio.Transmit{At: time.Now(), What: "SEND_CHANNEL_TXT_MSG"})
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

// packet builds a real on-air packet: header, path_len, hop hashes, then an opaque payload.
func (s *Sim) packet() ([]byte, string) {
	type kind struct {
		route, payload byte
		weight         int
	}
	kinds := []kind{
		{meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, 30},
		{meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, 22},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, 12},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeAck, 14},
		{meshcore.RouteTypeFlood, meshcore.PayloadTypePath, 6},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, 6},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeResponse, 6},
		{meshcore.RouteTypeFlood, meshcore.PayloadTypeTrace, 4},
	}
	total := 0
	for _, k := range kinds {
		total += k.weight
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pick := s.rng.Intn(total)
	var k kind
	for _, k = range kinds {
		if pick < k.weight {
			break
		}
		pick -= k.weight
	}
	hops := 1 + s.rng.Intn(4)
	path := make([]byte, hops)
	for i := range path {
		path[i] = repeaters[s.rng.Intn(len(repeaters))].hash
	}
	payload := make([]byte, 10+s.rng.Intn(40))
	s.rng.Read(payload)
	p := &meshcore.Packet{Header: meshcore.MakeHeader(k.route, k.payload, 0), PathLength: meshcore.MakePathLen(1, uint8(hops)), Path: path, Payload: payload}
	raw, err := p.ToBytes()
	if err != nil {
		return nil, ""
	}
	desc := ""
	if k.payload == meshcore.PayloadTypeAdvert && s.rng.Intn(3) == 0 {
		r := repeaters[s.rng.Intn(len(repeaters))]
		desc = fmt.Sprintf("ADVERT  %s [%02X]  %d hops", r.name, r.hash, hops)
	}
	return raw, desc
}

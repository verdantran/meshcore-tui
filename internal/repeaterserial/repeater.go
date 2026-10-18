package repeaterserial

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

var ErrConsoleOnly = errors.New("this is a repeater's own console; messaging and contacts need a companion radio")

const (
	cmdTimeout   = 10 * time.Second
	logTimeout   = 60 * time.Second
	defaultRetry = 2 * time.Second
)

var retryEvery = defaultRetry

// Repeater is a node on its serial console, shaped as a radio. It's its own only contact,
// already logged in as admin, and requests become console commands.
type Repeater struct {
	port   string
	reopen func() (io.ReadWriteCloser, error)
	events chan radio.Event

	mu     sync.Mutex
	con    *Console
	self   radio.Contact
	tag    uint32
	cancel context.CancelFunc
	once   sync.Once
}

var errNotRepeater = fmt.Errorf("%w; for a companion radio, choose companion mode on the connect screen", ErrNoAnswer)

// Dial opens a repeater's console directly, for when it was chosen on the connect screen.
func Dial(port string) *Repeater {
	return NewRepeater(port, nil, func() (io.ReadWriteCloser, error) { return openPort(port) })
}

// NewRepeater takes over a console already found by Probe, or opens one with reopen when con is nil.
// reopen is also used after the node reboots.
func NewRepeater(port string, con *Console, reopen func() (io.ReadWriteCloser, error)) *Repeater {
	return &Repeater{port: port, con: con, reopen: reopen, events: make(chan radio.Event, 256)}
}

func (r *Repeater) Events() <-chan radio.Event { return r.events }
func (r *Repeater) Describe() string           { return r.port + " · repeater console" }
func (r *Repeater) Transport() string          { return "USB" }

func (r *Repeater) emit(e radio.Event) {
	select {
	case r.events <- e:
	default:
	}
}

func (r *Repeater) Close() error {
	var err error
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		r.mu.Lock()
		if r.con != nil {
			err = r.con.Close()
		}
		r.mu.Unlock()
	})
	return err
}

func (r *Repeater) console() *Console {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.con
}

func (r *Repeater) run(ctx context.Context, cmd string) (string, error) {
	lines, err := r.lines(ctx, cmd, nil, cmdTimeout)
	return Reply(lines), err
}

func (r *Repeater) lines(ctx context.Context, cmd string, until func(string) bool, timeout time.Duration) ([]string, error) {
	con := r.console()
	if con == nil {
		return nil, ErrClosed
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lines, err := con.Run(ctx, cmd, until)
	if errors.Is(err, ErrClosed) {
		r.lost()
	}
	return lines, err
}

func (r *Repeater) get(ctx context.Context, key string) string {
	out, err := r.run(ctx, "get "+key)
	if err != nil {
		return ""
	}
	v, err := admin.ParseGet(out)
	if err != nil {
		return ""
	}
	return v
}

func (r *Repeater) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	go func() {
		step := func(key, label, detail string, s radio.StepState) {
			r.emit(radio.BootStep{Key: key, Label: label, Detail: detail, State: s})
		}
		if r.console() == nil {
			step("port", "PORT", r.port, radio.StepWait)
			rw, err := r.reopen()
			if err != nil {
				step("port", "PORT", err.Error(), radio.StepFail)
				r.emit(radio.Fatal{Err: err})
				return
			}
			r.mu.Lock()
			r.con = NewConsole(rw)
			r.mu.Unlock()
			step("port", "PORT", r.port+" · 115200 baud", radio.StepOK)
		}
		step("console", "CONSOLE", "", radio.StepWait)
		ver, err := r.run(ctx, "ver")
		if err != nil {
			if err == ErrNoAnswer {
				err = errNotRepeater
			}
			step("console", "CONSOLE", err.Error(), radio.StepFail)
			r.emit(radio.Fatal{Err: err})
			return
		}
		fw, built, _ := strings.Cut(ver, " (Build: ")
		board, _ := r.run(ctx, "board")
		step("console", "CONSOLE", "repeater "+fw+" · "+board, radio.StepOK)

		step("identity", "IDENTITY", "", radio.StepWait)
		self := radio.Contact{Name: r.get(ctx, "name"), Type: radio.TypeRepeater, PathLen: 0, LastAdvert: time.Now()}
		if r.get(ctx, "role") == "room_server" {
			self.Type = radio.TypeRoom
		}
		if k, err := hex.DecodeString(r.get(ctx, "public.key")); err == nil && len(k) == 32 {
			self.Key = [32]byte(k)
		}
		self.Lat, _ = strconv.ParseFloat(r.get(ctx, "lat"), 64)
		self.Lon, _ = strconv.ParseFloat(r.get(ctx, "lon"), 64)
		r.mu.Lock()
		r.self = self
		r.mu.Unlock()
		step("identity", "IDENTITY", self.Name+" · "+radio.KeyHex(self.Key[:4]), radio.StepOK)

		info := radio.SelfInfo{Name: self.Name, PubKey: self.Key, Lat: self.Lat, Lon: self.Lon}
		if f := strings.Split(r.get(ctx, "radio"), ","); len(f) == 4 {
			freq, _ := strconv.ParseFloat(f[0], 64)
			bw, _ := strconv.ParseFloat(f[1], 64)
			sf, _ := strconv.Atoi(f[2])
			cr, _ := strconv.Atoi(f[3])
			info.FreqKHz, info.BwHz, info.SF, info.CR = uint32(freq*1000), uint32(bw*1000), byte(sf), byte(cr)
		}
		tx, _ := strconv.Atoi(r.get(ctx, "tx"))
		info.TxPower = byte(tx)
		var core coreStats
		if out, err := r.run(ctx, "stats-core"); err == nil {
			json.Unmarshal([]byte(out), &core)
		}
		r.emit(radio.Ready{Self: info, Device: radio.DeviceInfo{Model: board, Firmware: fw, BuildDate: strings.TrimSuffix(built, ")")},
			Contacts: 1, Battery: radio.Battery{MilliVolts: core.Battery}, ContactList: []radio.Contact{self}, Console: true})
	}()
}

// lost reports a closed port and keeps trying to reopen it, as it does when the node reboots.
func (r *Repeater) lost() {
	r.mu.Lock()
	if r.reopen == nil || r.con == nil {
		r.mu.Unlock()
		return
	}
	old := r.con
	r.con = nil
	r.mu.Unlock()
	old.Close()
	r.emit(radio.Lost{Err: ErrClosed})
	go func() {
		for {
			time.Sleep(retryEvery)
			rw, err := r.reopen()
			if err != nil {
				continue
			}
			r.mu.Lock()
			r.con = NewConsole(rw)
			r.mu.Unlock()
			r.emit(radio.Restored{})
			return
		}
	}()
}

func (r *Repeater) prefix() [6]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.self.Prefix()
}

func (r *Repeater) Login(ctx context.Context, key [32]byte, password string) error {
	r.emit(radio.LoginResult{Prefix: r.prefix(), OK: true, Admin: true, Permissions: 3})
	return nil
}

func (r *Repeater) Logout(ctx context.Context, key [32]byte) error { return nil }

type coreStats struct {
	Battery uint16 `json:"battery_mv"`
	Uptime  uint32 `json:"uptime_secs"`
	Errors  uint16 `json:"errors"`
	Queue   uint16 `json:"queue_len"`
}

type radioStats struct {
	Noise int16   `json:"noise_floor"`
	RSSI  int16   `json:"last_rssi"`
	SNR   float64 `json:"last_snr"`
	TxAir uint32  `json:"tx_air_secs"`
	RxAir uint32  `json:"rx_air_secs"`
}

type packetStats struct {
	Recv       uint32 `json:"recv"`
	Sent       uint32 `json:"sent"`
	FloodTx    uint32 `json:"flood_tx"`
	DirectTx   uint32 `json:"direct_tx"`
	FloodRx    uint32 `json:"flood_rx"`
	DirectRx   uint32 `json:"direct_rx"`
	RecvErrors uint32 `json:"recv_errors"`
}

// RequestStatus assembles the over-the-air status reply from the console's three stats commands.
func (r *Repeater) RequestStatus(ctx context.Context, key [32]byte) error {
	var core coreStats
	var rad radioStats
	var pkt packetStats
	for cmd, into := range map[string]any{"stats-core": &core, "stats-radio": &rad, "stats-packets": &pkt} {
		out, err := r.run(ctx, cmd)
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(out), into); err != nil {
			return fmt.Errorf("%s: %w", cmd, err)
		}
	}
	st := meshcore.RepeaterStats{CommonStats: meshcore.CommonStats{BattMilliVolts: core.Battery, TxQueueLen: core.Queue,
		NoiseFloor: rad.Noise, LastRSSI: rad.RSSI, PacketsRecv: pkt.Recv, PacketsSent: pkt.Sent, AirTimeSecs: rad.TxAir,
		UpTimeSecs: core.Uptime, SentFlood: pkt.FloodTx, SentDirect: pkt.DirectTx, RecvFlood: pkt.FloodRx, RecvDirect: pkt.DirectRx,
		ErrEvents: core.Errors, LastSNR: int16(rad.SNR * 4)}, RxAirTimeSecs: rad.RxAir, RecvErrors: pkt.RecvErrors}
	r.emit(radio.StatusReport{Prefix: r.prefix(), Data: meshcore.BuildRepeaterStats(st)})
	return nil
}

// RequestTelemetry reports the battery, the one reading the console always has.
func (r *Repeater) RequestTelemetry(ctx context.Context, key [32]byte) error {
	out, err := r.run(ctx, "stats-core")
	if err != nil {
		return err
	}
	var core coreStats
	json.Unmarshal([]byte(out), &core)
	lpp := meshcore.NewLPPEncoder()
	lpp.AddVoltage(1, float64(core.Battery)/1000)
	r.emit(radio.Telemetry{Prefix: r.prefix(), LPP: lpp.Bytes()})
	return nil
}

// BinaryRequest answers neighbour, access-list and owner requests from console output, in the over-the-air formats.
func (r *Repeater) BinaryRequest(ctx context.Context, key [32]byte, data []byte) (uint32, error) {
	if len(data) == 0 {
		return 0, errors.New("empty request")
	}
	r.mu.Lock()
	r.tag++
	tag := r.tag
	r.mu.Unlock()
	var reply []byte
	switch data[0] {
	case meshcore.ReqTypeGetNeighbours:
		out, err := r.run(ctx, "neighbors")
		if err != nil {
			return 0, err
		}
		reply = meshcore.BuildNeighboursReply(ParseNeighbours(out))
	case meshcore.ReqTypeGetAccessList:
		lines, err := r.lines(ctx, "get acl", nil, cmdTimeout)
		if err != nil {
			return 0, err
		}
		reply = meshcore.BuildAccessListReply(ParseACL(lines))
	case meshcore.ReqTypeGetOwnerInfo:
		ver, err := r.run(ctx, "ver")
		if err != nil {
			return 0, err
		}
		fw, _, _ := strings.Cut(ver, " (")
		reply = meshcore.BuildOwnerInfoReply(meshcore.OwnerInfo{FirmwareVersion: fw, Name: r.get(ctx, "name"), Owner: r.get(ctx, "owner.info")})
	default:
		return 0, fmt.Errorf("request type %d isn't available on the console", data[0])
	}
	go r.emit(radio.BinaryReply{Tag: tag, Data: reply}) // after the caller has the tag
	return tag, nil
}

// ParseNeighbours reads `neighbors` output: one "keyprefix:seconds-ago:snr" per line, SNR in quarter dB.
func ParseNeighbours(out string) meshcore.NeighboursReply {
	var nr meshcore.NeighboursReply
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(l), ":")
		if len(f) != 3 {
			continue
		}
		p, err := hex.DecodeString(f[0])
		secs, err2 := strconv.Atoi(f[1])
		snr, err3 := strconv.Atoi(f[2])
		if err != nil || err2 != nil || err3 != nil || len(p) != 4 {
			continue
		}
		nr.Neighbours = append(nr.Neighbours, meshcore.Neighbour{Prefix: p, HeardSecsAgo: uint32(secs), SNR: int8(snr)})
	}
	nr.Total = uint16(len(nr.Neighbours))
	return nr
}

// ParseACL reads `get acl`, which prints "ACL:" then "level fullkey" per client.
func ParseACL(lines []string) []meshcore.ACLEntry {
	var out []meshcore.ACLEntry
	for _, l := range lines {
		lvl, key, ok := strings.Cut(strings.TrimSpace(l), " ")
		perm, err := strconv.ParseUint(lvl, 16, 8)
		k, err2 := hex.DecodeString(key)
		if !ok || err != nil || err2 != nil || len(k) < 6 {
			continue
		}
		out = append(out, meshcore.ACLEntry{Prefix: [6]byte(k[:6]), Permissions: byte(perm)})
	}
	return out
}

// SendCLI runs a command and hands back its output as a console reply.
func (r *Repeater) SendCLI(ctx context.Context, key [32]byte, command string) error {
	go func() {
		timeout, until := cmdTimeout, (func(string) bool)(nil)
		if strings.TrimSpace(command) == "log" {
			timeout, until = logTimeout, func(l string) bool { return strings.HasSuffix(strings.TrimSpace(l), "EOF") }
		}
		lines, err := r.lines(context.Background(), command, until, timeout)
		text := Reply(lines)
		switch {
		case errors.Is(err, ErrClosed):
			if admin.Silent(command) {
				return
			}
			text = "the port closed: " + err.Error()
		case err != nil && text == "":
			text = "Error: " + err.Error()
		case text == "":
			if admin.Silent(command) {
				return
			}
			text = "(no output)"
		}
		r.reply(text)
	}()
	return nil
}

func (r *Repeater) reply(text string) {
	r.emit(radio.Incoming{At: time.Now(), From: r.prefix(), Hops: -1, SentAt: time.Now(), Text: text, CLI: true})
}

func (r *Repeater) SyncContacts(ctx context.Context) ([]radio.Contact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []radio.Contact{r.self}, nil
}

func (r *Repeater) SendText(context.Context, [32]byte, string) (radio.Sent, error) {
	return radio.Sent{}, ErrConsoleOnly
}
func (r *Repeater) SendChannel(context.Context, int, string) error   { return ErrConsoleOnly }
func (r *Repeater) ResetPath(context.Context, [32]byte) error        { return ErrConsoleOnly }
func (r *Repeater) SaveContact(context.Context, radio.Contact) error { return ErrConsoleOnly }
func (r *Repeater) RemoveContact(context.Context, [32]byte) error    { return ErrConsoleOnly }
func (r *Repeater) Trace(context.Context, []byte) (uint32, error)    { return 0, ErrConsoleOnly }
func (r *Repeater) DiscoverPath(context.Context, [32]byte) error     { return ErrConsoleOnly }
func (r *Repeater) ExportContact(context.Context, [32]byte) (string, error) {
	return "", ErrConsoleOnly
}
func (r *Repeater) ImportContact(context.Context, string) error { return ErrConsoleOnly }
func (r *Repeater) AutoAdd(context.Context) (radio.AutoAdd, error) {
	return radio.AutoAdd{}, ErrConsoleOnly
}
func (r *Repeater) SetAutoAdd(context.Context, radio.AutoAdd) error { return ErrConsoleOnly }
func (r *Repeater) AdvertPath(context.Context, [32]byte) (radio.AdvertPath, error) {
	return radio.AdvertPath{}, ErrConsoleOnly
}

// Device is nil: a repeater's console has no companion settings.
func (r *Repeater) Device() radio.Device { return nil }

func (r *Repeater) Channels(context.Context) ([]radio.Channel, error) { return nil, ErrConsoleOnly }
func (r *Repeater) SetChannel(context.Context, radio.Channel) error   { return ErrConsoleOnly }

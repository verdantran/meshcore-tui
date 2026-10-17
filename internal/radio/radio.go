// Package radio connects to a companion radio and turns everything it does into a stream of events.
package radio

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/OwlShack/meshcore-go/companion"
	"slices"
	"strings"
	"time"
)

// Radio is a connected companion: the SDK-backed Link, or the simulator.
type Radio interface {
	Start(ctx context.Context)
	Events() <-chan Event
	Close() error
	Describe() string  // what we're connected to, e.g. a port or host
	Transport() string // USB, TCP, BLE or SIM

	SendText(ctx context.Context, to [32]byte, text string) (Sent, error)
	SendChannel(ctx context.Context, idx int, text string) error
	ResetPath(ctx context.Context, contact [32]byte) error

	// Contacts and routes.
	SyncContacts(ctx context.Context) ([]Contact, error)
	SaveContact(ctx context.Context, c Contact) error // add, or update the route and flags
	RemoveContact(ctx context.Context, key [32]byte) error
	Trace(ctx context.Context, path []byte) (tag uint32, err error) // answered by a TraceResult
	DiscoverPath(ctx context.Context, key [32]byte) error           // answered by a PathFound
	AdvertPath(ctx context.Context, key [32]byte) (AdvertPath, error)
	ExportContact(ctx context.Context, key [32]byte) (string, error) // a meshcore:// link
	ImportContact(ctx context.Context, link string) error
	AutoAdd(ctx context.Context) (AutoAdd, error)
	SetAutoAdd(ctx context.Context, a AutoAdd) error

	// Remote administration of repeaters and room servers. The radio tracks one outstanding
	// login, status or binary request at a time; a new one cancels the last, so callers queue them.
	Login(ctx context.Context, key [32]byte, password string) error // answered by a LoginResult
	Logout(ctx context.Context, key [32]byte) error
	RequestStatus(ctx context.Context, key [32]byte) error                        // answered by a StatusReport
	RequestTelemetry(ctx context.Context, key [32]byte) error                     // answered by a Telemetry
	BinaryRequest(ctx context.Context, key [32]byte, data []byte) (uint32, error) // answered by a BinaryReply with this tag
	SendCLI(ctx context.Context, key [32]byte, command string) error              // replies arrive as Incoming with CLI set

	Channels(ctx context.Context) ([]Channel, error)
	SetChannel(ctx context.Context, c Channel) error // an empty name and secret frees the slot

	Device() Device // the companion's own settings and counters; nil when there's no companion
}

// Signer is a radio that can sign channel messages with its own key, and send and receive the datagrams
// that carry the signatures (firmware v1.15 or later).
type Signer interface {
	// SendChannelAt sends a channel message and reports the timestamp it went with, which its signature must use.
	SendChannelAt(ctx context.Context, idx int, text string) (uint32, error)
	Sign(ctx context.Context, data []byte) ([64]byte, error) // the radio signs with its key, which never leaves it
	SendChannelData(ctx context.Context, idx int, dataType uint16, data []byte) error
}

// ChannelData is a binary datagram heard on a channel; signatures for channel messages arrive this way.
type ChannelData struct {
	At   time.Time
	Idx  int
	SNR  float32
	Type uint16
	Data []byte
}

// LoginResult answers a login. Permissions is the server's level: 0 guest, 1 read-only, 2 read-write, 3 admin.
type LoginResult struct {
	Prefix      [6]byte
	OK          bool
	Admin       bool
	Permissions byte
}

// StatusReport is a GET_STATUS reply body, decoded by whoever knows if it came from a repeater or a room.
type StatusReport struct {
	Prefix [6]byte
	Data   []byte
}

// Telemetry is a Cayenne LPP reading set from a remote node.
type Telemetry struct {
	Prefix [6]byte
	LPP    []byte
}

type BinaryReply struct {
	Tag  uint32
	Data []byte
}

// FlagFavourite marks a contact as a favourite in its flags byte.
const FlagFavourite byte = 0x01

// LinkPrefix starts a contact link: the hex of the radio's exported advert, as the official apps use.
const LinkPrefix = "meshcore://"

// AdvertPath is the route a contact's latest advert took to reach us.
type AdvertPath struct {
	At   time.Time
	Path []byte
}

// AutoAdd controls which kinds of newly heard node the radio adds as contacts by itself.
type AutoAdd struct {
	Chat, Repeater, Room, Sensor bool
	OverwriteOldest              bool // when full, replace the least recently heard non-favourite
	MaxHops                      int  // 0 means no limit
}

// Heard is a node whose advert arrived but which isn't a contact (auto-add is off for it, or the table is full).
type Heard struct{ Contact Contact }

// ContactRemoved is sent when the radio dropped a contact to make room.
type ContactRemoved struct{ Key [32]byte }

// ContactsFull is sent when a new node couldn't be added because the table is full.
type ContactsFull struct{}

// TraceResult carries the signal at each hop of a trace; Tag matches the Trace call.
type TraceResult struct {
	Tag     uint32
	Hashes  []byte
	SNRs    []float32 // what each hop heard, in path order
	LastSNR float32   // what we heard on the way back
}

// PathFound is a fresh route from path discovery, in both directions.
type PathFound struct {
	Prefix [6]byte
	Out    []byte
	In     []byte
}

// MaxText is the firmware's MAX_TEXT_LEN in bytes. Channel messages share it with the "name: " prefix.
const MaxText = 160

type ContactType byte

const (
	TypeChat     ContactType = 1
	TypeRepeater ContactType = 2
	TypeRoom     ContactType = 3
	TypeSensor   ContactType = 4
)

func (t ContactType) String() string {
	switch t {
	case TypeChat:
		return "Chat"
	case TypeRepeater:
		return "Repeater"
	case TypeRoom:
		return "Room"
	case TypeSensor:
		return "Sensor"
	}
	return "Unknown"
}

type Contact struct {
	Key        [32]byte
	Name       string
	Type       ContactType
	Flags      byte
	PathLen    int // hops on the stored direct route; -1 means none, so sends flood
	Path       []byte
	LastAdvert time.Time
	Lat, Lon   float64
}

func (c Contact) Prefix() [6]byte {
	var p [6]byte
	copy(p[:], c.Key[:6])
	return p
}

func KeyHex(k []byte) string { return strings.ToUpper(hex.EncodeToString(k)) }

type Channel struct {
	Idx    int
	Name   string
	Secret [16]byte
}

// Incoming is a received text message, from a contact or a channel.
type Incoming struct {
	At         time.Time
	Channel    bool
	ChannelIdx int
	From       [6]byte // sender's key prefix, for direct messages
	Hops       int     // -1 when it came over a direct route
	SentAt     time.Time
	Text       string // channel messages arrive as "name: text"
	SNR        float32
	HasSNR     bool
	Signed     bool
	Author     [4]byte // for signed messages, such as room posts: the writer's key prefix
	CLI        bool    // a reply from a repeater's command console, not a chat message
}

// Sent is the radio's receipt for a direct message; Ack matches the later Acked event.
type Sent struct {
	Ack     uint32
	Flood   bool
	Timeout time.Duration
}

type Acked struct {
	Ack uint32
	RTT time.Duration
}

// ContactUpdated carries a new or changed contact, such as after an advert or a path update.
type ContactUpdated struct{ Contact Contact }

type Event any

type StepState int

const (
	StepWait StepState = iota
	StepOK
	StepFail
)

// BootStep is one stage of the connect sequence. The same Key is sent again when it finishes.
type BootStep struct {
	Key    string
	Label  string
	Detail string
	State  StepState
}

type SelfInfo struct {
	Name     string
	PubKey   [32]byte
	FreqKHz  uint32
	BwHz     uint32
	SF, CR   byte
	TxPower  byte
	Lat, Lon float64
}

// Radio params formatted like the header: 869.618 SF8 BW62.5.
func (s SelfInfo) Params() string {
	return fmt.Sprintf("%.3f SF%d BW%s", float64(s.FreqKHz)/1000, s.SF, trimFloat(float64(s.BwHz)/1000))
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.1f", f)
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}

type DeviceInfo struct {
	Model       string
	Firmware    string
	BuildDate   string
	MaxContacts int
	MaxChannels int
}

// Ready is sent once the connect sequence has finished.
type Ready struct {
	Self        SelfInfo
	Device      DeviceInfo
	Contacts    int
	Channels    int
	Battery     Battery
	ContactList []Contact
	ChannelList []Channel
	Console     bool // a repeater's own serial console rather than a companion radio
}

type Battery struct {
	MilliVolts uint16
}

// Chemistry picks the discharge curve a battery's percentage is read from.
type Chemistry string

const (
	LiIon   Chemistry = "li-ion"
	LiPo    Chemistry = "lipo"
	LiFePO4 Chemistry = "lifepo4"
	Wired   Chemistry = "none" // powered without a battery, so there's no charge to read
)

var Chemistries = []Chemistry{LiIon, LiPo, LiFePO4, Wired}

func (c Chemistry) String() string {
	switch c {
	case LiPo:
		return "LiPo"
	case LiFePO4:
		return "LiFePO4"
	case Wired:
		return "None (wired)"
	}
	return "Li-ion (18650)"
}

// Next is the chemistry after c, for cycling through them.
func (c Chemistry) Next() Chemistry {
	i := slices.Index(Chemistries, c)
	return Chemistries[(i+1)%len(Chemistries)]
}

// curves give a single cell's millivolts at 100%, 90% … 0%. Li-ion and LiFePO4 are Meshtastic's
// tables, taken under a mesh radio's light load; LiPo is a typical pouch cell's at rest.
var curves = map[Chemistry][11]float64{
	LiIon:   {4190, 4050, 3990, 3890, 3800, 3720, 3630, 3530, 3420, 3300, 3100},
	LiPo:    {4200, 4130, 4060, 3990, 3930, 3870, 3820, 3790, 3770, 3730, 3500},
	LiFePO4: {3400, 3350, 3320, 3290, 3270, 3260, 3250, 3230, 3200, 3120, 3000},
}

// Percent is the charge of a Li-ion cell.
func (b Battery) Percent() int { return b.PercentOf(LiIon) }

// PercentOf reads the charge off c's curve, interpolating between its points.
func (b Battery) PercentOf(c Chemistry) int {
	curve, ok := curves[c]
	if !ok {
		curve = curves[LiIon]
	}
	mv := float64(b.MilliVolts)
	if mv >= curve[0] {
		return 100
	}
	for i := 1; i < len(curve); i++ {
		if hi, lo := curve[i-1], curve[i]; mv >= lo {
			return int(100 - 10*float64(i) + 10*(mv-lo)/(hi-lo) + 0.5)
		}
	}
	return 0
}

// Packet is a raw on-air packet the radio heard (PUSH_CODE_LOG_RX_DATA).
type Packet struct {
	At   time.Time
	SNR  float32
	RSSI int8
	Raw  []byte
}

// Transmit marks something going out over the air, for the TX lamp.
type Transmit struct {
	At   time.Time
	What string
}

type Tone int

const (
	ToneInfo Tone = iota
	ToneOK
	ToneWarn
	ToneFail
)

// Log is a line for the system log, such as a path update or an advert.
type Log struct {
	At   time.Time
	Text string
	Tone Tone
}

// Lost and Restored bracket a dropped connection; the transport reconnects on its own.
type Lost struct{ Err error }
type Restored struct{}

// Fatal means the connection can't be made at all.
type Fatal struct{ Err error }

type Dir int

const (
	ToRadio Dir = iota
	FromRadio
)

// Frame is one companion-protocol frame, for the debug pane.
type Frame struct {
	At    time.Time
	Dir   Dir
	Code  byte
	Name  string
	Bytes []byte // command bytes; responses arrive parsed, so these are empty for FromRadio
	// Hidden is the [start, end) of Bytes that carried a secret; those bytes are zeroed and shown as ··.
	Hidden [2]int
}

// CommandFrame records a command for the frames panel, with any secret in it zeroed out first.
func CommandFrame(at time.Time, cmd []byte) Frame {
	f := Frame{At: at, Dir: ToRadio, Code: cmd[0], Name: CommandName(cmd[0]), Bytes: append([]byte(nil), cmd...)}
	start, end := secretBytes(cmd)
	end = min(end, len(cmd))
	if start < end {
		clear(f.Bytes[start:end])
		f.Hidden = [2]int{start, end}
	}
	return f
}

// secretBytes is where a command carries a private key, PIN, password, channel secret or console text.
func secretBytes(cmd []byte) (int, int) {
	switch cmd[0] {
	case companion.CmdImportPrivateKey, companion.CmdSetDevicePin:
		return 1, len(cmd)
	case companion.CmdSendLogin:
		return 33, len(cmd) // after the node's public key
	case companion.CmdSetChannel:
		return 34, 50 // after the slot and the name
	case companion.CmdSendTxtMsg:
		if len(cmd) > 1 && cmd[1] == companion.TxtTypeCLIData {
			return 13, len(cmd) // console commands can set passwords and keys
		}
	}
	return 0, 0
}

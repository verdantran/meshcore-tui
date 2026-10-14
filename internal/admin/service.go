package admin

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
)

type LoginState int

const (
	LoggedOut LoginState = iota
	LoggingIn
	LoggedIn
	LoginFailed
)

type JobKind int

const (
	JobLogin JobKind = iota
	JobStatus
	JobTelemetry
	JobNeighbours
	JobACL
	JobOwner
)

func (k JobKind) String() string {
	return [...]string{"login", "status", "telemetry", "neighbours", "access list", "owner info"}[k]
}

// Job is one radio request. The radio tracks only one at a time, so they run in turn.
type Job struct {
	ID       int
	Kind     JobKind
	Key      [32]byte
	Password string
	Data     []byte // the binary request body, for neighbour, access-list and owner requests
	tag      uint32
	started  time.Time
}

// Command is a console command waiting to go out, or waiting for its reply.
type Command struct {
	ID      int
	Key     [32]byte
	Text    string
	Purpose string // "", "get <key>" or "set <key>", so replies can fill in the settings form
	Silent  bool   // the node won't reply, such as reboot
	sentAt  time.Time
}

type StatusPoint struct {
	At    time.Time
	Stats meshcore.RepeaterStats
	Room  *meshcore.RoomStats
}

type NeighbourRow struct {
	Prefix []byte
	Name   string
	Heard  time.Duration
	SNR    float32
}

type ACLRow struct {
	Prefix [6]byte
	Name   string
	Level  byte
}

type Reading struct {
	Label string
	Value string
	Num   float64
	IsNum bool
}

type Value struct {
	Value   string
	Err     string
	At      time.Time
	Loading bool
}

type Line struct {
	At    time.Time
	Out   bool // typed by you; replies are false
	Text  string
	Error bool
}

type Session struct {
	Key   [32]byte
	Name  string
	Type  radio.ContactType
	State LoginState
	Admin bool
	Level byte
	Since time.Time

	Status       []StatusPoint
	Telemetry    []Reading
	TelemetryAt  time.Time
	TelemetryLog map[string][]float64 // numeric readings over time, by label
	Neighbours   []NeighbourRow
	NeighbourAt  time.Time
	NeighbourAll int
	ACL          []ACLRow
	ACLAt        time.Time
	Owner        *meshcore.OwnerInfo
	Regions      []Region
	RegionsAt    time.Time
	Settings     map[string]Value
	Console      []Line
	Pending      map[string]string // staged edits, key → new value

	inflight *Command
	queue    []*Command
}

func (s *Session) Latest() (StatusPoint, bool) {
	if len(s.Status) == 0 {
		return StatusPoint{}, false
	}
	return s.Status[len(s.Status)-1], true
}

// Busy reports whether this node has console work outstanding.
func (s *Session) Busy() bool { return s.inflight != nil || len(s.queue) > 0 }

// Names resolves key prefixes to contact names.
type Names func(prefix []byte) string

const (
	JobTimeout      = 25 * time.Second
	CommandTimeout  = 20 * time.Second
	historyKeep     = 288 // a day of five-minute polls
	consoleKeep     = 500
	neighbourPrefix = 4 // what a repeater's console prints, so local and remote lists match
)

type Service struct {
	names    Names
	sessions map[[32]byte]*Session
	jobs     []*Job
	running  *Job
	nextID   int
	random   func() [4]byte
}

func New(names Names) *Service {
	return &Service{names: names, sessions: map[[32]byte]*Session{}, random: func() [4]byte {
		var b [4]byte
		rand.Read(b[:])
		return b
	}}
}

// Session returns the node's session, creating an empty one the first time.
func (s *Service) Session(c radio.Contact) *Session {
	if sess, ok := s.sessions[c.Key]; ok {
		sess.Name, sess.Type = c.Name, c.Type
		return sess
	}
	sess := &Session{Key: c.Key, Name: c.Name, Type: c.Type, Settings: map[string]Value{}, Pending: map[string]string{}, TelemetryLog: map[string][]float64{}}
	s.sessions[c.Key] = sess
	return sess
}

func (s *Service) Find(key [32]byte) (*Session, bool) {
	sess, ok := s.sessions[key]
	return sess, ok
}

func (s *Service) byPrefix(p []byte) (*Session, bool) {
	for k, sess := range s.sessions {
		if string(k[:len(p)]) == string(p) {
			return sess, true
		}
	}
	return nil, false
}

// enqueue adds a job and returns the one to start now, if the radio is free.
func (s *Service) enqueue(j *Job, now time.Time) *Job {
	s.nextID++
	j.ID = s.nextID
	s.jobs = append(s.jobs, j)
	return s.startNext(now)
}

func (s *Service) startNext(now time.Time) *Job {
	if s.running != nil || len(s.jobs) == 0 {
		return nil
	}
	s.running, s.jobs = s.jobs[0], s.jobs[1:]
	s.running.started = now
	return s.running
}

// finish ends the running job and returns the next to start.
func (s *Service) finish(id int, now time.Time) *Job {
	if s.running == nil || s.running.ID != id {
		return nil
	}
	s.running = nil
	return s.startNext(now)
}

// Queued is how many radio requests are waiting behind the one in flight.
func (s *Service) Queued() int { return len(s.jobs) }

func (s *Service) Running() (*Job, bool) { return s.running, s.running != nil }

func (s *Service) Login(c radio.Contact, password string, now time.Time) *Job {
	sess := s.Session(c)
	sess.State = LoggingIn
	return s.enqueue(&Job{Kind: JobLogin, Key: c.Key, Password: password}, now)
}

func (s *Service) Logout(key [32]byte) {
	if sess, ok := s.sessions[key]; ok {
		sess.State, sess.Admin, sess.Level = LoggedOut, false, 0
		sess.queue, sess.inflight = nil, nil
	}
}

func (s *Service) RequestStatus(c radio.Contact, now time.Time) *Job {
	s.Session(c)
	return s.enqueue(&Job{Kind: JobStatus, Key: c.Key}, now)
}

func (s *Service) RequestTelemetry(c radio.Contact, now time.Time) *Job {
	s.Session(c)
	return s.enqueue(&Job{Kind: JobTelemetry, Key: c.Key}, now)
}

func (s *Service) RequestNeighbours(c radio.Contact, now time.Time) *Job {
	s.Session(c)
	body := meshcore.BuildNeighboursRequest(meshcore.NeighboursRequest{Count: 32, OrderBy: meshcore.NeighboursStrongestFirst, PrefixLen: neighbourPrefix}, s.random())
	return s.enqueue(&Job{Kind: JobNeighbours, Key: c.Key, Data: body}, now)
}

func (s *Service) RequestACL(c radio.Contact, now time.Time) *Job {
	s.Session(c)
	return s.enqueue(&Job{Kind: JobACL, Key: c.Key, Data: meshcore.BuildRequest(meshcore.ReqTypeGetAccessList, s.random())}, now)
}

func (s *Service) RequestOwner(c radio.Contact, now time.Time) *Job {
	s.Session(c)
	return s.enqueue(&Job{Kind: JobOwner, Key: c.Key, Data: meshcore.BuildRequest(meshcore.ReqTypeGetOwnerInfo, s.random())}, now)
}

// Sent records the tag a binary request went out with, so its reply can be matched.
func (s *Service) Sent(id int, tag uint32) {
	if s.running != nil && s.running.ID == id {
		s.running.tag = tag
	}
}

// Failed ends a job whose request the radio refused.
func (s *Service) Failed(id int, err error, now time.Time) *Job {
	if s.running != nil && s.running.ID == id {
		if sess, ok := s.sessions[s.running.Key]; ok && s.running.Kind == JobLogin {
			sess.State = LoginFailed
		}
	}
	return s.finish(id, now)
}

// TimedOut ends a job whose reply never came. It reports whether the job was still waiting.
func (s *Service) TimedOut(id int, now time.Time) (*Job, bool) {
	if s.running == nil || s.running.ID != id {
		return nil, false
	}
	if sess, ok := s.sessions[s.running.Key]; ok && s.running.Kind == JobLogin {
		sess.State = LoginFailed
	}
	return s.finish(id, now), true
}

// OnLogin applies a login result. It returns the session it was for and the next job to start.
func (s *Service) OnLogin(r radio.LoginResult, now time.Time) (*Session, *Job) {
	sess, ok := s.byPrefix(r.Prefix[:])
	if !ok {
		return nil, nil
	}
	if r.OK {
		sess.State, sess.Admin, sess.Level, sess.Since = LoggedIn, r.Admin, r.Permissions, now
	} else {
		sess.State = LoginFailed
	}
	return sess, s.finishFor(JobLogin, sess.Key, now)
}

// finishFor ends the running job if it's this kind for this node.
func (s *Service) finishFor(kind JobKind, key [32]byte, now time.Time) *Job {
	if s.running != nil && s.running.Kind == kind && s.running.Key == key {
		return s.finish(s.running.ID, now)
	}
	return nil
}

func (s *Service) OnStatus(r radio.StatusReport, now time.Time) (*Session, *Job) {
	sess, ok := s.byPrefix(r.Prefix[:])
	if !ok {
		return nil, nil
	}
	point := StatusPoint{At: now}
	if sess.Type == radio.TypeRoom {
		if room, err := meshcore.ParseRoomStats(r.Data); err == nil {
			point.Stats.CommonStats, point.Room = room.CommonStats, &room
		}
	} else if st, err := meshcore.ParseRepeaterStats(r.Data); err == nil {
		point.Stats = st
	}
	sess.Status = keep(append(sess.Status, point), historyKeep)
	return sess, s.finishFor(JobStatus, sess.Key, now)
}

func (s *Service) OnTelemetry(r radio.Telemetry, now time.Time) (*Session, *Job) {
	sess, ok := s.byPrefix(r.Prefix[:])
	if !ok {
		return nil, nil
	}
	sess.Telemetry, sess.TelemetryAt = DescribeLPP(r.LPP), now
	for _, rd := range sess.Telemetry {
		if rd.IsNum {
			sess.TelemetryLog[rd.Label] = keep(append(sess.TelemetryLog[rd.Label], rd.Num), historyKeep)
		}
	}
	return sess, s.finishFor(JobTelemetry, sess.Key, now)
}

// OnBinary matches a binary reply to the request that's waiting for it.
func (s *Service) OnBinary(r radio.BinaryReply, now time.Time) (*Session, *Job) {
	j := s.running
	if j == nil || j.tag != r.Tag || j.Kind < JobNeighbours {
		return nil, nil
	}
	sess := s.sessions[j.Key]
	switch j.Kind {
	case JobNeighbours:
		if nb, err := meshcore.ParseNeighboursReply(r.Data, neighbourPrefix); err == nil {
			sess.Neighbours, sess.NeighbourAll, sess.NeighbourAt = nil, int(nb.Total), now
			for _, n := range nb.Neighbours {
				sess.Neighbours = append(sess.Neighbours, NeighbourRow{Prefix: n.Prefix, Name: s.names(n.Prefix),
					Heard: time.Duration(n.HeardSecsAgo) * time.Second, SNR: float32(n.SNR) / 4})
			}
		}
	case JobACL:
		if entries, err := meshcore.ParseAccessListReply(r.Data); err == nil {
			sess.ACL, sess.ACLAt = nil, now
			for _, e := range entries {
				sess.ACL = append(sess.ACL, ACLRow{Prefix: e.Prefix, Name: s.names(e.Prefix[:]), Level: e.Permissions & 0x03})
			}
		}
	case JobOwner:
		if oi, err := meshcore.ParseOwnerInfoReply(r.Data); err == nil {
			sess.Owner = &oi
		}
	}
	return sess, s.finish(j.ID, now)
}

// Command queues console text for a node and returns it if it can go out now.
// Replies carry no ID, so each node has one command in flight and replies are matched in order.
func (s *Service) Command(key [32]byte, text, purpose string, now time.Time) *Command {
	sess, ok := s.sessions[key]
	if !ok {
		return nil
	}
	s.nextID++
	cmd := &Command{ID: s.nextID, Key: key, Text: text, Purpose: purpose, Silent: Silent(text)}
	sess.Console = appendLine(sess.Console, Line{At: now, Out: true, Text: text})
	if strings.HasPrefix(purpose, "get ") {
		k := strings.TrimPrefix(purpose, "get ")
		v := sess.Settings[k]
		v.Loading = true
		sess.Settings[k] = v
	}
	sess.queue = append(sess.queue, cmd)
	return s.nextCommand(sess, now)
}

// Silent reports whether a command makes the node go away without replying.
func Silent(text string) bool {
	switch strings.TrimSpace(text) {
	case "reboot", "poweroff", "shutdown", "clkreboot", "start ota":
		return true
	}
	return false
}

func (s *Service) nextCommand(sess *Session, now time.Time) *Command {
	if sess.inflight != nil || len(sess.queue) == 0 {
		return nil
	}
	sess.inflight, sess.queue = sess.queue[0], sess.queue[1:]
	sess.inflight.sentAt = now
	return sess.inflight
}

func appendLine(lines []Line, l Line) []Line { return keep(append(lines, l), consoleKeep) }

func keep[T any](s []T, n int) []T {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// CommandSent is called once the radio has taken a command; one that expects no reply finishes here.
func (s *Service) CommandSent(c *Command, err error, now time.Time) *Command {
	sess, ok := s.sessions[c.Key]
	if !ok || sess.inflight != c {
		return nil
	}
	if err != nil {
		s.settle(sess, "couldn't send: "+err.Error(), true, now)
		return s.nextCommand(sess, now)
	}
	if c.Silent {
		sess.Console = appendLine(sess.Console, Line{At: now, Text: "sent; the node doesn't reply to this"})
		sess.inflight = nil
		return s.nextCommand(sess, now)
	}
	return nil
}

// OnReply files a console reply against the command it answers. It returns that command and the next one to send.
func (s *Service) OnReply(from [6]byte, text string, now time.Time) (sess *Session, done, next *Command) {
	sess, ok := s.byPrefix(from[:])
	if !ok {
		return nil, nil, nil
	}
	if sess.inflight == nil {
		sess.Console = appendLine(sess.Console, Line{At: now, Text: text}) // unsolicited, keep it anyway
		return sess, nil, nil
	}
	done = sess.inflight
	s.settle(sess, text, false, now)
	return sess, done, s.nextCommand(sess, now)
}

// CommandTimedOut gives up on a command that got no reply.
func (s *Service) CommandTimedOut(key [32]byte, id int, now time.Time) *Command {
	sess, ok := s.sessions[key]
	if !ok || sess.inflight == nil || sess.inflight.ID != id {
		return nil
	}
	s.settle(sess, fmt.Sprintf("no reply after %s", CommandTimeout), true, now)
	return s.nextCommand(sess, now)
}

func (s *Service) settle(sess *Session, text string, failed bool, now time.Time) {
	c := sess.inflight
	sess.inflight = nil
	errLine := failed || strings.HasPrefix(text, "Error") || strings.HasPrefix(text, "Unknown")
	sess.Console = appendLine(sess.Console, Line{At: now, Text: text, Error: errLine})
	switch {
	case c.Purpose == "region" && !failed:
		sess.Regions, sess.RegionsAt = ParseRegions(text), now
	case strings.HasPrefix(c.Purpose, "get "):
		k := strings.TrimPrefix(c.Purpose, "get ")
		v := Value{At: now}
		if failed {
			v.Err = text
		} else if val, err := ParseGet(text); err != nil {
			v.Err = err.Error()
		} else {
			v.Value = val
		}
		sess.Settings[k] = v
	case strings.HasPrefix(c.Purpose, "set "):
		k := strings.TrimPrefix(c.Purpose, "set ")
		if failed {
			return
		}
		if err := ParseSet(text); err == nil {
			sess.Settings[k] = Value{Value: sess.Pending[k], At: now}
			delete(sess.Pending, k)
		} else {
			v := sess.Settings[k]
			v.Err = err.Error()
			sess.Settings[k] = v
		}
	}
}

// ReadSettings asks the node for every setting in a group, one command at a time.
func (s *Service) ReadSettings(key [32]byte, group string, now time.Time) *Command {
	sess, ok := s.sessions[key]
	if !ok {
		return nil
	}
	var first *Command
	for _, st := range Settings(sess.Type) {
		if group != "" && st.Group != group {
			continue
		}
		if c := s.Command(key, "get "+st.Key, "get "+st.Key, now); c != nil && first == nil {
			first = c
		}
	}
	return first
}

// Stage records an edit to apply later, after checking it against the schema.
func (s *Service) Stage(key [32]byte, setting, value string) error {
	sess, ok := s.sessions[key]
	if !ok {
		return fmt.Errorf("not logged in")
	}
	st, ok := Lookup(setting)
	if !ok {
		return fmt.Errorf("unknown setting %q", setting)
	}
	if err := st.Validate(value); err != nil {
		return err
	}
	if cur, ok := sess.Settings[setting]; ok && cur.Value == value {
		delete(sess.Pending, setting)
		return nil
	}
	sess.Pending[setting] = value
	return nil
}

// Change is one staged edit, for the diff shown before applying.
type Change struct {
	Setting  Setting
	From, To string
}

func (s *Service) Changes(key [32]byte) []Change {
	sess, ok := s.sessions[key]
	if !ok {
		return nil
	}
	var out []Change
	for _, st := range Settings(sess.Type) {
		if to, ok := sess.Pending[st.Key]; ok {
			out = append(out, Change{Setting: st, From: sess.Settings[st.Key].Value, To: to})
		}
	}
	return out
}

// Apply sends every staged edit. It reports whether any of them needs a reboot to take effect.
func (s *Service) Apply(key [32]byte, now time.Time) (*Command, bool) {
	var first *Command
	reboot := false
	for _, ch := range s.Changes(key) {
		reboot = reboot || ch.Setting.Reboot
		if c := s.Command(key, "set "+ch.Setting.Key+" "+ch.Setting.Wire(ch.To), "set "+ch.Setting.Key, now); c != nil && first == nil {
			first = c
		}
	}
	return first, reboot
}

// DescribeLPP turns Cayenne LPP telemetry into labelled readings.
func DescribeLPP(data []byte) []Reading {
	readings, err := meshcore.LPPDecode(data)
	if err != nil {
		return []Reading{{Label: "telemetry", Value: "couldn't decode: " + err.Error()}}
	}
	var out []Reading
	for _, r := range readings {
		label, format := lppNames[r.Type].label, lppNames[r.Type].format
		if label == "" {
			label, format = fmt.Sprintf("type %d", r.Type), "%v"
		}
		num, isNum := r.Value.(float64)
		out = append(out, Reading{Label: fmt.Sprintf("%s (ch %d)", label, r.Channel), Value: fmt.Sprintf(format, r.Value), Num: num, IsNum: isNum})
	}
	return out
}

var lppNames = map[byte]struct{ label, format string }{
	meshcore.LPPVoltage:     {"voltage", "%.2f V"},
	meshcore.LPPTemperature: {"temperature", "%.1f °C"},
	meshcore.LPPCurrent:     {"current", "%.3f A"},
	meshcore.LPPPercentage:  {"level", "%v %%"},
	meshcore.LPPLuminosity:  {"light", "%v lux"},
	meshcore.LPPAltitude:    {"altitude", "%.1f m"},
	meshcore.LPPPower:       {"power", "%v W"},
	meshcore.LPPFrequency:   {"frequency", "%v Hz"},
}

var levelNames = []string{"guest", "read-only", "read-write", "admin"}

// LevelName is the name of an access level.
func LevelName(l byte) string {
	if int(l) < len(levelNames) {
		return levelNames[l]
	}
	return fmt.Sprintf("level %d", l)
}

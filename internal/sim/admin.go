package sim

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// Passwords for the simulated servers. An empty password logs in as a guest.
const (
	adminPassword = "password"
	roomPassword  = "room"
)

type post struct {
	author string
	text   string
	at     time.Time
}

// server is one simulated repeater or room server's state.
type server struct {
	name     string
	typ      radio.ContactType
	settings map[string]string
	logging  bool
	acl      map[string]byte // key prefix hex → permission level
	regions  []admin.Region
}

func (s *Sim) serverFor(k [32]byte) (*server, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if srv, ok := s.servers[k]; ok {
		return srv, true
	}
	i := s.known(k)
	if i < 0 || s.book[i].Type != radio.TypeRepeater && s.book[i].Type != radio.TypeRoom {
		return nil, false
	}
	c := s.book[i]
	alice := key(0x3A, "Alice")
	srv := &server{name: c.Name, typ: c.Type, settings: map[string]string{},
		acl:     map[string]byte{radio.KeyHex(device.self.PubKey[:6]): 3, radio.KeyHex(alice[:6]): 1},
		regions: []admin.Region{{Name: "*", Flood: true}, {Name: "bristol", Depth: 1, Home: true, Flood: true}, {Name: "bath", Depth: 2, Flood: true}, {Name: "wales", Depth: 1}}}
	for _, st := range admin.Settings(c.Type) {
		srv.settings[st.Key] = st.Default
	}
	srv.settings["name"] = c.Name
	srv.settings["role"] = map[radio.ContactType]string{radio.TypeRepeater: "repeater", radio.TypeRoom: "room_server"}[c.Type]
	srv.settings["public.key"] = radio.KeyHex(c.Key[:])
	srv.settings["lat"], srv.settings["lon"] = fmt.Sprintf("%.4f", c.Lat), fmt.Sprintf("%.4f", c.Lon)
	s.servers[k] = srv
	return srv, true
}

func (s *Sim) level(k [32]byte) (byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.sessions[k]
	return l, ok
}

func (s *Sim) after(ms int, e radio.Event) {
	go func() {
		time.Sleep(s.delay(ms))
		s.emit(e)
	}()
}

func (s *Sim) Login(ctx context.Context, k [32]byte, password string) error {
	srv, ok := s.serverFor(k)
	if !ok {
		return errors.New("not a repeater or room server")
	}
	s.frame(26, 6)
	s.emit(radio.Transmit{At: time.Now(), What: "SEND_LOGIN"})
	want := adminPassword
	if srv.typ == radio.TypeRoom {
		want = roomPassword
	}
	var level byte
	switch password {
	case want:
		level = 3
	case "":
		level = 0 // guest
	default:
		s.after(900, radio.LoginResult{Prefix: prefix(k)})
		return nil
	}
	s.mu.Lock()
	s.sessions[k] = level
	s.mu.Unlock()
	s.after(900, radio.LoginResult{Prefix: prefix(k), OK: true, Admin: level == 3, Permissions: level})
	if srv.typ == radio.TypeRoom {
		go s.syncRoom(k)
	}
	return nil
}

// syncRoom sends a room's posts after login, the way a room server pushes what you haven't seen.
func (s *Sim) syncRoom(k [32]byte) {
	time.Sleep(s.delay(1500))
	s.mu.Lock()
	posts := append([]post(nil), s.posts...)
	s.mu.Unlock()
	for _, p := range posts {
		if p.author == device.self.Name {
			continue // a room server doesn't send you your own posts
		}
		time.Sleep(s.delay(250))
		ak := key(peopleHash(p.author), p.author)
		author := [4]byte(ak[:4])
		s.emit(radio.Incoming{At: time.Now(), From: prefix(k), Hops: 2, SentAt: p.at, Text: p.text, Signed: true, Author: author, SNR: 4, HasSNR: true})
	}
}

func (s *Sim) Logout(ctx context.Context, k [32]byte) error {
	s.mu.Lock()
	delete(s.sessions, k)
	s.mu.Unlock()
	s.frame(29, 0)
	return nil
}

func (s *Sim) uptime() uint32 { return uint32(time.Since(s.born).Seconds()) + 1051200 }

// counters grow like a busy repeater's, at a rate that drifts over the hour, so polls show changing rates.
// They start at the same values for every node, shifted by a few percent.
func (s *Sim) counters(shift int) meshcore.RepeaterStats {
	e := time.Since(s.born).Seconds()
	grow := func(base, rate float64) uint32 {
		return uint32(base*(1+float64(shift)/100) + e*rate + rate*600*(1-math.Cos(e/600)))
	}
	recv := grow(48210, 0.12)
	return meshcore.RepeaterStats{CommonStats: meshcore.CommonStats{BattMilliVolts: 4105 - uint16(min(e/3600, 200)), LastRSSI: -88,
		NoiseFloor: int16(-113 + math.Round(2*math.Sin(e/1800))), PacketsRecv: recv, PacketsSent: grow(12004, 0.03),
		AirTimeSecs: grow(3200, 0.012), UpTimeSecs: s.uptime(), SentFlood: grow(9881, 0.024), SentDirect: grow(2123, 0.006),
		RecvFlood: recv * 3 / 4, RecvDirect: recv / 4, FloodDups: uint16(grow(31440, 0.03)), DirectDups: 120},
		RxAirTimeSecs: grow(11000, 0.05), RecvErrors: grow(14, 0.002)}
}

// RequestStatus answers anyone logged in; others get no reply, as a real server ignores them.
func (s *Sim) RequestStatus(ctx context.Context, k [32]byte) error {
	srv, ok := s.serverFor(k)
	if !ok {
		return errors.New("not a repeater or room server")
	}
	s.frame(27, 6)
	if _, in := s.level(k); !in {
		return nil
	}
	s.mu.Lock()
	noise := int16(-115 + s.rng.Intn(6))
	s.mu.Unlock()
	st := s.counters(int(k[1]) % 40)
	st.BattMilliVolts, st.NoiseFloor, st.LastSNR = uint16(4050+int(k[1])%60), noise, 38
	var data []byte
	if srv.typ == radio.TypeRoom {
		data = meshcore.BuildRoomStats(meshcore.RoomStats{CommonStats: st.CommonStats, Posted: uint16(len(s.posts)), PostPushes: 12})
	} else {
		data = meshcore.BuildRepeaterStats(st)
	}
	s.after(1100, radio.StatusReport{Prefix: prefix(k), Data: data})
	return nil
}

func (s *Sim) RequestTelemetry(ctx context.Context, k [32]byte) error {
	if _, ok := s.serverFor(k); !ok {
		return errors.New("not a repeater or room server")
	}
	s.frame(39, 6)
	if _, in := s.level(k); !in {
		return nil
	}
	lpp := meshcore.NewLPPEncoder()
	lpp.AddVoltage(1, float64(4050+int(k[1])%60)/1000)
	lpp.AddTemperature(2, 14.5+float64(k[2]%8))
	s.after(1200, radio.Telemetry{Prefix: prefix(k), LPP: lpp.Bytes()})
	return nil
}

// BinaryRequest answers neighbour and access-list requests with the real reply formats.
func (s *Sim) BinaryRequest(ctx context.Context, k [32]byte, data []byte) (uint32, error) {
	srv, ok := s.serverFor(k)
	if !ok || len(data) == 0 {
		return 0, errors.New("not a repeater or room server")
	}
	s.mu.Lock()
	s.tag++
	tag := s.tag
	s.mu.Unlock()
	s.frame(50, 6)
	level, in := s.level(k)
	if !in {
		return tag, nil
	}
	var reply []byte
	switch data[0] {
	case meshcore.ReqTypeGetNeighbours:
		req, err := meshcore.ParseNeighboursRequest(data)
		if err != nil {
			return tag, nil
		}
		var ns []meshcore.Neighbour
		for i, r := range repeaters {
			if r.name == srv.name || i%2 == int(k[1])%2 {
				continue
			}
			kk := key(r.hash, r.name)
			ns = append(ns, meshcore.Neighbour{Prefix: kk[:req.PrefixLen], HeardSecsAgo: uint32(60 * (i + 1)), SNR: int8(linkSNR(k[0], r.hash) * 4)})
		}
		sort.Slice(ns, func(a, b int) bool { return ns[a].SNR > ns[b].SNR })
		reply = meshcore.BuildNeighboursReply(meshcore.NeighboursReply{Total: uint16(len(ns)), Neighbours: ns})
	case meshcore.ReqTypeGetAccessList:
		if level < 3 {
			return tag, nil
		}
		var entries []meshcore.ACLEntry
		keys := make([]string, 0, len(srv.acl))
		for hexKey := range srv.acl {
			keys = append(keys, hexKey)
		}
		sort.Strings(keys)
		for _, hexKey := range keys {
			var p [6]byte
			fmt.Sscanf(hexKey, "%02X%02X%02X%02X%02X%02X", &p[0], &p[1], &p[2], &p[3], &p[4], &p[5])
			entries = append(entries, meshcore.ACLEntry{Prefix: p, Permissions: srv.acl[hexKey]})
		}
		reply = meshcore.BuildAccessListReply(entries)
	case meshcore.ReqTypeGetOwnerInfo:
		reply = meshcore.BuildOwnerInfoReply(meshcore.OwnerInfo{FirmwareVersion: device.info.Firmware, Name: srv.name, Owner: srv.settings["owner.info"]})
	default:
		return tag, nil
	}
	s.after(1000, radio.BinaryReply{Tag: tag, Data: reply})
	return tag, nil
}

// SendCLI runs a console command on a simulated server. Only admins get replies, as on the firmware.
func (s *Sim) SendCLI(ctx context.Context, k [32]byte, command string) error {
	srv, ok := s.serverFor(k)
	if !ok {
		return errors.New("not a repeater or room server")
	}
	s.frame(2, 6) // console text can hold passwords, so it isn't recorded
	if level, in := s.level(k); !in || level < 3 {
		return nil
	}
	reply, silent := s.console(srv, strings.TrimSpace(command))
	if silent {
		return nil
	}
	s.after(800+s.intn(600), radio.Incoming{At: time.Now(), From: prefix(k), Hops: -1, SentAt: time.Now(), Text: reply, CLI: true})
	return nil
}

func (s *Sim) console(srv *server, cmd string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "get "):
		k := strings.TrimPrefix(cmd, "get ")
		if v, ok := srv.settings[k]; ok {
			return "> " + v, false
		}
		return "Unknown config: " + k, false
	case strings.HasPrefix(cmd, "set "):
		k, v, _ := strings.Cut(strings.TrimPrefix(cmd, "set "), " ")
		st, ok := admin.Lookup(k)
		if !ok {
			return "Unknown config: " + k, false
		}
		v = st.FromWire(v)
		if err := st.Validate(v); err != nil {
			return "Error: " + err.Error(), false
		}
		srv.settings[k] = v
		if st.Reboot {
			return "OK - reboot to apply", false
		}
		return "OK", false
	case cmd == "ver":
		return device.info.Firmware + " (Build: " + device.info.BuildDate + ")", false
	case cmd == "board":
		return device.info.Model, false
	case cmd == "clock":
		return time.Now().UTC().Format("15:04 - 2/1/2006 UTC"), false
	case cmd == "clock sync":
		return "OK - clock set: " + time.Now().UTC().Format("15:04:05 - 2/1/2006 UTC"), false
	case cmd == "advert":
		return "OK - Advert sent", false
	case cmd == "advert.zerohop":
		return "OK - zerohop advert sent", false
	case cmd == "reboot", cmd == "poweroff", cmd == "start ota":
		return "", true // the node goes away without replying
	case cmd == "log start":
		srv.logging = true
		return "OK - logging started", false
	case cmd == "log stop":
		srv.logging = false
		return "OK - logging stopped", false
	case cmd == "log erase":
		return "OK - log erased", false
	case cmd == "clear stats":
		return "OK - stats reset", false
	case strings.HasPrefix(cmd, "password "):
		return "password now: " + strings.TrimPrefix(cmd, "password "), false
	case cmd == "discover.neighbors", strings.HasPrefix(cmd, "neighbor.remove "):
		return "OK", false
	case strings.HasPrefix(cmd, "setperm "):
		return setperm(srv, strings.Fields(cmd)[1:]), false
	case cmd == "region":
		return exportRegions(srv.regions), false
	case strings.HasPrefix(cmd, "region "):
		return regionCmd(srv, strings.Fields(cmd)[1:]), false
	}
	return "Unknown command", false
}

// postToRoom stores a post sent to a simulated room server.
func (s *Sim) postToRoom(text string) {
	s.mu.Lock()
	s.posts = append(s.posts, post{author: device.self.Name, text: text, at: time.Now()})
	s.mu.Unlock()
}

func seededPosts(now time.Time) []post {
	return []post{
		{"Alice", "Welcome to the Clifton room. Be nice!", now.Add(-26 * time.Hour)},
		{"Bob", "Kelston repeater is getting a new antenna on Saturday", now.Add(-5 * time.Hour)},
		{"Carol", "anyone want to meet at Cabot Tower for a range test?", now.Add(-40 * time.Minute)},
	}
}

// setperm follows the firmware: level 0 removes by key prefix; any other level needs the whole key.
func setperm(srv *server, args []string) string {
	if len(args) < 2 {
		return "Err - bad params"
	}
	hexKey := strings.ToUpper(args[0])
	var lvl byte
	fmt.Sscanf(args[1], "%d", &lvl)
	if lvl&3 == 0 {
		for k := range srv.acl {
			if strings.HasPrefix(k, hexKey) || strings.HasPrefix(hexKey, k) {
				delete(srv.acl, k)
				return "OK"
			}
		}
		return "Err - invalid params"
	}
	if len(hexKey) < 64 {
		return "Err - invalid params"
	}
	srv.acl[hexKey[:12]] = lvl
	return "OK"
}

func exportRegions(rs []admin.Region) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(strings.Repeat(" ", r.Depth) + r.Name)
		if r.Home {
			b.WriteString("^")
		}
		if r.Flood {
			b.WriteString(" F")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func regionCmd(srv *server, args []string) string {
	find := func(name string) int {
		for i, r := range srv.regions {
			if r.Name == name {
				return i
			}
		}
		return -1
	}
	if len(args) < 2 {
		if len(args) == 1 && args[0] == "save" {
			return "OK"
		}
		return "Err - ??"
	}
	i := find(args[1])
	switch args[0] {
	case "put":
		parent := 0
		if len(args) > 2 {
			if parent = find(args[2]); parent < 0 {
				return "Err - unknown parent"
			}
		}
		at := parent + 1
		for at < len(srv.regions) && srv.regions[at].Depth > srv.regions[parent].Depth {
			at++
		}
		srv.regions = slices.Insert(srv.regions, at, admin.Region{Name: args[1], Depth: srv.regions[parent].Depth + 1, Flood: true})
		return "OK - (flood allowed)"
	case "remove":
		if i <= 0 {
			return "Err - not found"
		}
		if i+1 < len(srv.regions) && srv.regions[i+1].Depth > srv.regions[i].Depth {
			return "Err - not empty"
		}
		srv.regions = slices.Delete(srv.regions, i, i+1)
		return "OK"
	}
	if i < 0 {
		return "Err - unknown region"
	}
	switch args[0] {
	case "allowf", "denyf":
		srv.regions[i].Flood = args[0] == "allowf"
		return "OK"
	case "home":
		for j := range srv.regions {
			srv.regions[j].Home = j == i
		}
		return " home is now " + args[1]
	}
	return "Err - ??"
}

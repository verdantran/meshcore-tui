package admin

import (
	"math"
	"strings"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
)

// poll is a repeater's status at t0 plus mins, having been up since boot.
func poll(mins int, up, recv, sent, air, dups, errs uint32) StatusPoint {
	return StatusPoint{At: t0.Add(time.Duration(mins) * time.Minute), Stats: meshcore.RepeaterStats{CommonStats: meshcore.CommonStats{
		UpTimeSecs: up, PacketsRecv: recv, PacketsSent: sent, AirTimeSecs: air, FloodDups: uint16(dups), NoiseFloor: -110,
		BattMilliVolts: 4000 + uint16(mins)}, RecvErrors: errs, RxAirTimeSecs: air * 3}}
}

func series(h Health, name string) Series {
	for _, s := range h.Series {
		if s.Name == name {
			return s
		}
	}
	return Series{}
}

func TestHealthRates(t *testing.T) {
	points := []StatusPoint{
		poll(-10, 1000, 100, 50, 10, 0, 0), // before the window: only an anchor for the first rate
		poll(0, 1600, 160, 80, 16, 6, 0),
		poll(10, 2200, 220, 110, 22, 12, 4),
		poll(20, 30, 0, 0, 0, 0, 0), // rebooted
		poll(30, 630, 30, 15, 3, 3, 0),
		poll(200, 630+10200, 1000, 500, 100, 10, 0), // 2h50m after the last: too far apart to place
	}
	h := HealthOf(points, t0, t0.Add(4*time.Hour), 8)
	if h.Reboots != 1 || h.Polls != 5 {
		t.Errorf("reboots %d polls %d", h.Reboots, h.Polls)
	}
	recv := series(h, "Received")
	if recv.Values[0] != 360 { // 60 then 60 more packets over 10-minute gaps, so 360 an hour
		t.Errorf("received in the first half hour = %v", recv.Values[0])
	}
	if recv.Values[1] != 180 || !math.IsNaN(recv.Values[2]) || !math.IsNaN(recv.Values[6]) {
		t.Errorf("received = %v", recv.Values)
	}
	if recv.Latest != 180 {
		t.Errorf("latest = %v", recv.Latest)
	}
	if d := series(h, "Duplicates"); d.Values[0] != 10 || d.Values[1] != 10 {
		t.Errorf("duplicates = %v", d.Values)
	}
	if e := series(h, "Receive errors"); math.Abs(e.Values[0]-4.0/124*100) > 1e-9 {
		t.Errorf("errors = %v", e.Values)
	}
	if tx := series(h, "Transmitting"); tx.Values[0] != 1 || tx.Values[1] != 0.5 || math.Abs(tx.Avg-15.0/1800*100) > 1e-9 {
		t.Errorf("tx duty = %v avg %v", tx.Values, tx.Avg)
	}
	if rx := series(h, "Receiving"); rx.Values[0] != 3 {
		t.Errorf("rx duty = %v", rx.Values)
	}
	if b := series(h, "Battery"); b.Latest != 4.2 || math.Abs(b.Values[0]-4.01) > 1e-9 {
		t.Errorf("battery = %v latest %v", b.Values, b.Latest)
	}
	if n := series(h, "Noise floor"); n.Avg != -110 {
		t.Errorf("noise = %v", n.Avg)
	}
}

func TestHealthRooms(t *testing.T) {
	a, b := poll(0, 100, 10, 5, 1, 0, 0), poll(10, 700, 70, 35, 7, 0, 0)
	a.Room, b.Room = &meshcore.RoomStats{}, &meshcore.RoomStats{}
	h := HealthOf([]StatusPoint{a, b}, t0, t0.Add(time.Hour), 4)
	if s := series(h, "Receive errors"); s.Name != "" {
		t.Error("rooms don't report receive errors")
	}
	if s := series(h, "Received"); s.Latest != 360 {
		t.Errorf("room received = %v", s.Latest)
	}
	if h := HealthOf(nil, t0, t0.Add(time.Hour), 4); len(h.Series) != 0 {
		t.Error("nothing polled")
	}
}

func TestPointFrom(t *testing.T) {
	rep := meshcore.BuildRepeaterStats(meshcore.RepeaterStats{CommonStats: meshcore.CommonStats{PacketsRecv: 9}, RecvErrors: 2})
	if p, ok := PointFrom(t0, false, rep); !ok || p.Stats.PacketsRecv != 9 || p.Stats.RecvErrors != 2 || p.Room != nil {
		t.Errorf("repeater = %+v %v", p, ok)
	}
	room := meshcore.BuildRoomStats(meshcore.RoomStats{CommonStats: meshcore.CommonStats{PacketsRecv: 4}, Posted: 3})
	if p, ok := PointFrom(t0, true, room); !ok || p.Room.Posted != 3 || p.Stats.PacketsRecv != 4 {
		t.Errorf("room = %+v %v", p, ok)
	}
	if _, ok := PointFrom(t0, false, []byte{1}); ok {
		t.Error("short repeater reply")
	}
	if _, ok := PointFrom(t0, true, []byte{1}); ok {
		t.Error("short room reply")
	}
}

const sampleLog = `12:01:07 - 14/10/2026 U: RX, len=42 (type=5, route=F, payload_len=30) SNR=9 RSSI=-88 score=1000
12:01:08 - 14/10/2026 U: TX, len=43 (type=5, route=F, payload_len=30)
12:03:00 - 14/10/2026 U: RX, len=60 (type=2, route=D, payload_len=40) SNR=-12 RSSI=-118 score=310 [3A -> 7C]
12:03:01 - 14/10/2026 U: TX, len=61 (type=2, route=D, payload_len=40) [3A -> 7C]
12:05:30 - 14/10/2026 U: RX, len=110 (type=4, route=F, payload_len=101) SNR=3 RSSI=-101 score=800
12:06:00 - 14/10/2026 U: RX, len=20 (type=1, route=D, payload_len=12) SNR=12 RSSI=-70 score=1000 [3A -> 01]
12:07:00 - 14/10/2026 U: TX FAIL!, len=20 (type=1, route=D, payload_len=12)
12:08:00 - 14/10/2026 U: RX, len=9 (type=13, route=F, payload_len=4) SNR=0 RSSI=-95 score=900
some boot noise
   EOF`

func TestPacketLog(t *testing.T) {
	l := ParseLog(sampleLog)
	if len(l.Entries) != 8 || l.Skipped != 1 {
		t.Fatalf("entries %d skipped %d", len(l.Entries), l.Skipped)
	}
	e := l.Entries[2]
	if e.Dir != "RX" || !e.Direct || e.Type != 2 || e.SNR != -12 || e.RSSI != -118 || e.Score != 310 || e.Src != 0x3A || e.Dst != 0x7C {
		t.Errorf("entry = %+v", e)
	}
	if !e.At.Equal(time.Date(2026, 10, 14, 12, 3, 0, 0, time.UTC)) {
		t.Errorf("at = %v", e.At)
	}
	if l.Entries[1].HasSignal || l.Entries[1].Src != -1 {
		t.Error("sent packets have no signal")
	}
	s := l.Summary()
	if s.RX != 5 || s.TX != 2 || s.Fail != 1 || s.Flood != 3 || s.Direct != 2 || s.Signals != 5 {
		t.Errorf("summary = %+v", s)
	}
	if s.Types[0].Name != "GRP_TXT" && s.Types[0].Name != "TXT_MSG" || s.Types[0].RX != 1 || s.Types[0].TX != 1 {
		t.Errorf("types = %+v", s.Types)
	}
	var names []string
	for _, tc := range s.Types {
		names = append(names, tc.Name)
	}
	if !strings.Contains(strings.Join(names, " "), "TYPE_13") {
		t.Errorf("unknown types are named by number: %v", names)
	}
	if s.SNR[0] != 1 || s.SNR[3] != 2 || s.SNR[4] != 1 || s.SNR[5] != 1 || s.SNRAvg != 12.0/5 {
		t.Errorf("snr bands %v avg %v", s.SNR, s.SNRAvg)
	}
	if len(s.Senders) != 1 || s.Senders[0] != (HashCount{0x3A, 2}) {
		t.Errorf("senders = %+v", s.Senders)
	}
	if !s.First.Equal(l.Entries[0].At) || !s.Last.Equal(l.Entries[7].At) {
		t.Error("span")
	}
	rx, tx := l.Buckets(s.First, s.Last, 2)
	if rx[0] != 2 || rx[1] != 3 || tx[0] != 2 || tx[1] != 0 {
		t.Errorf("buckets rx %v tx %v", rx, tx)
	}
	if rx, _ := l.Buckets(s.First, s.First, 3); rx[2] != 5 {
		t.Errorf("a log within one second lands in the last bucket: %v", rx)
	}
	if (PacketLog{}).Summary().RX != 0 {
		t.Error("empty")
	}
}

func TestLogReplyFillsSession(t *testing.T) {
	s := newService()
	s.Session(dundry)
	c := s.Command(dundry.Key, "log", "log", t0)
	sess, done, _ := s.OnReply(prefix(dundry), sampleLog, t0)
	if done != c || sess.PacketLog == nil || len(sess.PacketLog.Entries) != 8 || !sess.PacketLogAt.Equal(t0) {
		t.Fatalf("log = %+v", sess.PacketLog)
	}
	if last := sess.Console[len(sess.Console)-1].Text; !strings.Contains(last, "read 8 log entries") {
		t.Errorf("console = %q", last)
	}
}

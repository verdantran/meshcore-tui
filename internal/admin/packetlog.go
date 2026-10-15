package admin

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LogEntry is one line of a repeater's packet log, read over USB with `log`.
type LogEntry struct {
	At         time.Time
	Dir        string // RX, TX or FAIL
	Len        int
	Type       int
	Direct     bool
	PayloadLen int
	HasSignal  bool // received packets carry SNR, RSSI and score
	SNR, RSSI  int
	Score      int // 0 to 1000
	Src, Dst   int // one-byte hashes, -1 when the packet type doesn't name them
}

// PacketLog is a parsed log; Skipped counts lines that weren't packet entries.
type PacketLog struct {
	Entries []LogEntry
	Skipped int
}

// The firmware writes "12:01:07 - 14/10/2026 U: RX, len=42 (type=5, route=F, payload_len=30) SNR=9 RSSI=-88 score=1000 [3A -> 7C]".
var logLine = regexp.MustCompile(`^(\d{1,2}):(\d{2}):(\d{2}) - (\d{1,2})/(\d{1,2})/(\d{4}) U: (RX|TX|TX FAIL!), len=(\d+) \(type=(\d+), route=([DF]), payload_len=(\d+)\)` +
	`(?: SNR=(-?\d+) RSSI=(-?\d+) score=(-?\d+))?(?: \[([0-9A-Fa-f]{2}) -> ([0-9A-Fa-f]{2})\])?`)

func ParseLog(text string) PacketLog {
	var out PacketLog
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || l == "EOF" {
			continue
		}
		m := logLine.FindStringSubmatch(l)
		if m == nil {
			out.Skipped++
			continue
		}
		n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
		e := LogEntry{At: time.Date(n(6), time.Month(n(5)), n(4), n(1), n(2), n(3), 0, time.UTC),
			Dir: map[string]string{"RX": "RX", "TX": "TX", "TX FAIL!": "FAIL"}[m[7]],
			Len: n(8), Type: n(9), Direct: m[10] == "D", PayloadLen: n(11), Src: -1, Dst: -1}
		if m[12] != "" {
			e.HasSignal, e.SNR, e.RSSI, e.Score = true, n(12), n(13), n(14)
		}
		if m[15] != "" {
			src, _ := strconv.ParseUint(m[15], 16, 8)
			dst, _ := strconv.ParseUint(m[16], 16, 8)
			e.Src, e.Dst = int(src), int(dst)
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

var payloadNames = map[int]string{0: "REQ", 1: "RESPONSE", 2: "TXT_MSG", 3: "ACK", 4: "ADVERT", 5: "GRP_TXT", 6: "GRP_DATA",
	7: "ANON_REQ", 8: "PATH", 9: "TRACE", 10: "MULTIPART", 11: "CONTROL", 15: "RAW_CUSTOM"}

func PayloadName(t int) string {
	if n, ok := payloadNames[t]; ok {
		return n
	}
	return fmt.Sprintf("TYPE_%d", t)
}

type TypeCount struct {
	Name   string
	RX, TX int
}

type HashCount struct {
	Hash  byte
	Count int
}

// LogSummary is what a packet log says about a repeater's traffic.
type LogSummary struct {
	First, Last   time.Time
	RX, TX, Fail  int
	Flood, Direct int // received
	Types         []TypeCount
	SNR           []int // received packets in SNR bands: below -10, -10 to -5, -5 to 0, 0 to 5, 5 to 10, 10 and up
	SNRAvg        float64
	RSSIAvg       float64
	Signals       int
	Senders       []HashCount // the most frequent source hashes among received packets
}

var SNRBands = []string{"< -10", "-10…-5", "-5…0", "0…5", "5…10", "≥ 10"}

func snrBand(snr int) int {
	return min(max((snr+15)/5, 0), len(SNRBands)-1)
}

func (l PacketLog) Summary() LogSummary {
	var s LogSummary
	s.SNR = make([]int, len(SNRBands))
	types := map[string]*TypeCount{}
	senders := map[byte]int{}
	for i, e := range l.Entries {
		if i == 0 || e.At.Before(s.First) {
			s.First = e.At
		}
		if e.At.After(s.Last) {
			s.Last = e.At
		}
		name := PayloadName(e.Type)
		tc := types[name]
		if tc == nil {
			tc = &TypeCount{Name: name}
			types[name] = tc
		}
		switch e.Dir {
		case "RX":
			s.RX++
			tc.RX++
			if e.Direct {
				s.Direct++
			} else {
				s.Flood++
			}
			if e.HasSignal {
				s.Signals++
				s.SNRAvg += float64(e.SNR)
				s.RSSIAvg += float64(e.RSSI)
				s.SNR[snrBand(e.SNR)]++
			}
			if e.Src >= 0 {
				senders[byte(e.Src)]++
			}
		case "TX":
			s.TX++
			tc.TX++
		case "FAIL":
			s.Fail++
		}
	}
	if s.Signals > 0 {
		s.SNRAvg /= float64(s.Signals)
		s.RSSIAvg /= float64(s.Signals)
	}
	for _, tc := range types {
		if tc.RX+tc.TX > 0 {
			s.Types = append(s.Types, *tc)
		}
	}
	sort.Slice(s.Types, func(i, j int) bool {
		a, b := s.Types[i], s.Types[j]
		if a.RX+a.TX != b.RX+b.TX {
			return a.RX+a.TX > b.RX+b.TX
		}
		return a.Name < b.Name
	})
	for h, n := range senders {
		s.Senders = append(s.Senders, HashCount{h, n})
	}
	sort.Slice(s.Senders, func(i, j int) bool {
		if s.Senders[i].Count != s.Senders[j].Count {
			return s.Senders[i].Count > s.Senders[j].Count
		}
		return s.Senders[i].Hash < s.Senders[j].Hash
	})
	return s
}

// Buckets counts received and sent entries in n equal slices of the log's time span.
func (l PacketLog) Buckets(from, to time.Time, n int) (rx, tx []int) {
	rx, tx = make([]int, n), make([]int, n)
	span := to.Sub(from)
	for _, e := range l.Entries {
		i := n - 1
		if span > 0 {
			i = min(int(e.At.Sub(from)*time.Duration(n)/span), n-1)
		}
		if i < 0 {
			continue
		}
		switch e.Dir {
		case "RX":
			rx[i]++
		case "TX":
			tx[i]++
		}
	}
	return rx, tx
}

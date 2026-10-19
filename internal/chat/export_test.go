package chat

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

// exportWhole is Export as it was before it streamed: everything read first, then written in one go.
func exportWhole(s *Service, w io.Writer, convos []store.Convo, asJSON bool, verdict func(store.Message) string) int {
	var ms []store.Message
	for _, c := range convos {
		got, _ := s.store.Messages(s.device, c, 1<<30)
		ms = append(ms, got...)
	}
	if asJSON {
		out := make([]exported, 0, len(ms))
		for _, m := range ms {
			e := exported{Conversation: s.Title(m.Convo), From: m.Sender, Outgoing: m.Out, Text: m.Text,
				Sent: m.SentAt, Received: m.RecvAt, Status: stateNames[m.State], RoundTripMS: m.RTT.Milliseconds()}
			if m.Convo.IsChannel() {
				e.Signature, e.SignerKey, e.Verdict = sigNames[m.Sig], hex.EncodeToString(m.SigKey), sigText(m, verdict)
			}
			if !m.Out {
				hops := m.Hops
				e.Hops = &hops
				if m.HasSNR {
					snr := m.SNR
					e.SNR = &snr
				}
			}
			out = append(out, e)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(struct {
			App      string     `json:"app,omitempty"`
			Messages []exported `json:"messages"`
		}{s.App, out})
		return len(ms)
	}
	var last store.Convo
	for _, m := range ms {
		if m.Convo != last {
			if last != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "# %s\n\n", untrusted.Line(s.Title(m.Convo)))
			last = m.Convo
		}
		detail := ""
		switch {
		case m.Out && m.State == store.Delivered:
			detail = fmt.Sprintf("  (delivered, %.1fs)", m.RTT.Seconds())
		case m.Out && m.State == store.Failed:
			detail = "  (not delivered)"
		case !m.Out && m.Hops < 0:
			detail = "  (direct)"
		case !m.Out:
			detail = fmt.Sprintf("  (%d hops)", m.Hops)
		}
		if v := sigText(m, verdict); v != "" && m.Convo.IsChannel() {
			detail += "  [" + v + "]"
		}
		text := untrusted.Line(strings.ReplaceAll(untrusted.Text(m.Text), "\n", " ⏎ "))
		fmt.Fprintf(w, "[%s] %s: %s%s\n", m.RecvAt.Local().Format("2006-01-02 15:04:05"), untrusted.Line(m.Sender), text, detail)
	}
	return len(ms)
}

func TestStreamedExportIsByteForByteTheSame(t *testing.T) {
	s := newService(t, 1, false)
	s.App = "meshcore-tui v1.2.3 \"quoted\""
	verdict := func(m store.Message) string {
		if m.Sig == store.SigValid {
			return "✓ signed by the pinned key"
		}
		return ""
	}
	if _, err := s.ExportLater("", true, nil)(io.Discard); err != nil {
		t.Fatal(err)
	}
	var none, noneWant strings.Builder
	s.Export(&none, "", true, nil)
	exportWhole(s, &noneWant, nil, true, nil)
	if none.String() != noneWant.String() {
		t.Fatalf("empty JSON export = %q, want %q", none.String(), noneWant.String())
	}

	// Enough to cross Each's batches within a conversation and between them.
	for i := range 2600 {
		at := t0.Add(time.Duration(i) * time.Second)
		switch i % 4 {
		case 0, 1:
			m, _ := s.Incoming(radio.Incoming{At: at, Channel: true, ChannelIdx: i % 2, Hops: i % 5, SNR: 4.25, HasSNR: i%3 == 0,
				Text: fmt.Sprintf("Carol: <b>%d</b> & \"quoted\"\nsecond line", i)})
			if i%8 == 0 {
				s.SetSig(m.ID, store.SigValid, []byte{0xAB, byte(i)})
			}
		case 2:
			s.Incoming(radio.Incoming{At: at, From: alice.Prefix(), Text: fmt.Sprintf("dm %d", i), Hops: -1})
		default:
			_, send, err := s.Compose(store.ContactConvo(alice.Key), fmt.Sprintf("reply %d", i), at)
			if err != nil {
				t.Fatal(err)
			}
			if i%7 == 0 {
				s.Result(send, radio.Sent{}, fmt.Errorf("refused"))
			} else {
				s.Result(send, radio.Sent{Ack: uint32(i), Timeout: time.Second}, nil)
				s.Acked(radio.Acked{Ack: uint32(i), RTT: time.Duration(i) * time.Millisecond})
			}
		}
	}
	convos := []store.Convo{store.ChannelConvo(0), store.ChannelConvo(1), store.ContactConvo(alice.Key)}
	slices.Sort(convos)
	for _, tc := range []struct {
		c      store.Convo
		convos []store.Convo
	}{{"", convos}, {store.ChannelConvo(1), []store.Convo{store.ChannelConvo(1)}}, {store.ContactConvo(alice.Key), []store.Convo{store.ContactConvo(alice.Key)}}} {
		for _, asJSON := range []bool{false, true} {
			var got, want strings.Builder
			n, err := s.ExportLater(tc.c, asJSON, verdict)(&got)
			wantN := exportWhole(s, &want, tc.convos, asJSON, verdict)
			if err != nil || n != wantN || got.String() != want.String() {
				t.Errorf("export %q json=%v: n=%d want %d, err %v, same=%v", tc.c, asJSON, n, wantN, err, got.String() == want.String())
			}
		}
	}
}

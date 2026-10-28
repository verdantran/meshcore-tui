package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

const (
	opTimeout    = 15 * time.Second
	syncTimeout  = 2 * time.Minute
	restoreEach  = 2 * time.Second // more for each channel and contact a restore writes, one round trip apiece
	traceTimeout = 20 * time.Second
	traceReveal  = 900 * time.Millisecond
	detailRows   = 11
)

type overlay int

const (
	noOverlay overlay = iota
	shareOverlay
	editorOverlay
	importOverlay
	placeOverlay
	removeOverlay
	autoOverlay
	pruneOverlay
	importConfirmOverlay
)

var typeFilters = []struct {
	name string
	typ  radio.ContactType
	fav  bool
}{{"all", 0, false}, {"favourites", 0, true}, {"chat", radio.TypeChat, false}, {"repeater", radio.TypeRepeater, false},
	{"room", radio.TypeRoom, false}, {"sensor", radio.TypeSensor, false}}

var sortNames = []string{"last heard", "name", "hops"}

type heardNode struct {
	c  radio.Contact
	at time.Time
}

type traceRun struct {
	key     [32]byte
	path    []byte
	size    int // bytes per hop in path
	started time.Time
	result  *radio.TraceResult
	at      time.Time // when the result arrived, for the hop-by-hop reveal
	lost    bool
}

type contactsModel struct {
	sel        int
	typeFilter int
	sortBy     int
	filter     lineEdit
	filtering  bool
	heardView  bool
	heard      []heardNode
	heardIdx   map[[32]byte]int // where each key is in heard
	heardRev   int              // counts changes to heard nodes' names and keys
	heardSel   int
	full       bool

	advert      map[[32]byte]radio.AdvertPath
	traces      map[uint32]*traceRun
	lastTrace   map[[32]byte]uint32
	discovering map[[32]byte]time.Time
	arrived     map[[32]byte]entrance // rows new or freshly advertised, for their entrance
	found       map[[32]byte]radio.PathFound

	overlay   overlay
	link      string
	editPath  []byte
	editSize  int // bytes per hop in editPath
	editSel   int
	importIn  lineEdit
	importing linkContact // the link waiting for y, and who it says it is
	placeIn   lineEdit
	auto      radio.AutoAdd
	autoSel   int
	autoReady bool
	prune     [][32]byte
}

func newContacts() contactsModel {
	return contactsModel{advert: map[[32]byte]radio.AdvertPath{}, traces: map[uint32]*traceRun{},
		lastTrace: map[[32]byte]uint32{}, discovering: map[[32]byte]time.Time{}, arrived: map[[32]byte]entrance{}, found: map[[32]byte]radio.PathFound{},
		heardIdx: map[[32]byte]int{}}
}

// Messages from radio calls made on the Contacts screen.
type contactsSyncedMsg struct {
	list []radio.Contact
	err  error
}
type advertPathMsg struct {
	key [32]byte
	ap  radio.AdvertPath
	err error
}
type traceStartedMsg struct {
	key  [32]byte
	path []byte
	size int
	tag  uint32
	err  error
}
type traceTimeoutMsg struct{ tag uint32 }
type exportedMsg struct {
	link string
	err  error
}
type autoAddMsg struct {
	auto radio.AutoAdd
	err  error
}

// opDoneMsg reports a contact change; the table is re-synced afterwards so it matches the radio.
type opDoneMsg struct {
	what string
	err  error
}

func (m *Model) radioCall(f func(ctx context.Context) tea.Msg) tea.Cmd {
	return m.radioCallFor(opTimeout, f)
}

// radioCallFor is radioCall for work that takes longer than one operation, as a list or a restore does.
func (m *Model) radioCallFor(limit time.Duration, f func(ctx context.Context) tea.Msg) tea.Cmd {
	r := m.radio
	if r == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		return f(ctx)
	}
}

// syncContacts allows longer than opTimeout: a long list over Bluetooth arrives a contact at a time, and
// the radio link gives up on it by itself once it goes quiet.
func (m *Model) syncContacts() tea.Cmd {
	r := m.radio
	return m.radioCallFor(syncTimeout, func(ctx context.Context) tea.Msg {
		list, err := r.SyncContacts(ctx)
		return contactsSyncedMsg{list, err}
	})
}

func (m *Model) contactOp(what string, f func(ctx context.Context, r radio.Radio) error) tea.Cmd {
	r := m.radio
	return m.radioCall(func(ctx context.Context) tea.Msg { return opDoneMsg{what, f(ctx, r)} })
}

func typeColour(th theme.Theme, t radio.ContactType) theme.RGB {
	switch t {
	case radio.TypeChat:
		return th.Cyan
	case radio.TypeRoom:
		return th.Violet
	case radio.TypeSensor:
		return th.Green
	}
	return th.Text
}

// hashes shows a path hop by hop, size bytes to a hop.
func hashes(b []byte, size int) string {
	var parts []string
	for h := range slices.Chunk(b, max(size, 1)) {
		parts = append(parts, fmt.Sprintf("%X", h))
	}
	return strings.Join(parts, "·")
}

func routeText(c radio.Contact) string {
	if c.PathLen < 0 {
		return "flood"
	}
	if c.PathLen == 0 {
		return "direct, in range"
	}
	return fmt.Sprintf("direct %s", hashes(c.Path, c.HashSize))
}

// pathHash is how many bytes per hop the radio puts in the paths it starts.
func (m *Model) pathHash() int {
	if m.device.loaded {
		return m.device.s.PathHashMode + 1
	}
	return max(m.ready.Device.PathHash, 1)
}

// keyHash is a node's key cut to the radio's path hash, as its hops show it.
func (m *Model) keyHash(k [32]byte) string {
	return fmt.Sprintf("%X", k[:min(m.pathHash(), len(k))])
}

// rows is the contact table after the type filter, text filter and sort.
func (m *Model) contactRows() []radio.Contact {
	if m.chat == nil {
		return nil
	}
	c := &m.contacts
	var out []radio.Contact
	want, favOnly := typeFilters[c.typeFilter].typ, typeFilters[c.typeFilter].fav
	q := strings.ToLower(c.filter.text)
	for _, ct := range m.chat.Contacts() {
		if want != 0 && ct.Type != want || favOnly && ct.Flags&radio.FlagFavourite == 0 {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(ct.Name), q) && !strings.Contains(strings.ToLower(radio.KeyHex(ct.Key[:1])), q) {
			continue
		}
		out = append(out, ct)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if fa, fb := a.Flags&radio.FlagFavourite != 0, b.Flags&radio.FlagFavourite != 0; fa != fb {
			return fa // favourites first
		}
		switch c.sortBy {
		case 1:
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		case 2:
			ha, hb := a.PathLen, b.PathLen
			if ha < 0 {
				ha = 99
			}
			if hb < 0 {
				hb = 99
			}
			if ha != hb {
				return ha < hb
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.LastAdvert.After(b.LastAdvert)
	})
	return out
}

func (m *Model) selectedContact() (radio.Contact, bool) {
	rows := m.contactRows()
	if len(rows) == 0 {
		return radio.Contact{}, false
	}
	m.contacts.sel = min(max(m.contacts.sel, 0), len(rows)-1)
	return rows[m.contacts.sel], true
}

// onSelectContact fetches the advert path for the newly selected contact, once.
func (m *Model) onSelectContact() tea.Cmd {
	ct, ok := m.selectedContact()
	if !ok {
		return nil
	}
	if _, have := m.contacts.advert[ct.Key]; have {
		return nil
	}
	r := m.radio
	return m.radioCall(func(ctx context.Context) tea.Msg {
		ap, err := r.AdvertPath(ctx, ct.Key)
		return advertPathMsg{ct.Key, ap, err}
	})
}

// tracePath is the round trip for a contact: out along its route and back, so we hear each hop both ways.
// A repeater with no stored route is traced as a direct neighbour. Hops are the radio's size where they can
// be: no longer than the stored route's, which can't be lengthened, and a power of two, which is all a
// trace's flags can say.
func tracePath(ct radio.Contact, size int) ([]byte, int, bool) {
	if ct.PathLen > 0 {
		size = min(size, max(ct.HashSize, 1))
	}
	if size >= 2 {
		size = 2
	}
	var out [][]byte
	switch {
	case ct.PathLen > 0:
		for h := range slices.Chunk(ct.Path, max(ct.HashSize, 1)) {
			if len(h) < size {
				return nil, 0, false
			}
			out = append(out, h[:size])
		}
		if ct.Type == radio.TypeRepeater {
			out = append(out, ct.Key[:size])
		}
	case ct.Type == radio.TypeRepeater:
		out = [][]byte{ct.Key[:size]}
	default:
		return nil, 0, false
	}
	for i := len(out) - 2; i >= 0; i-- {
		out = append(out, out[i])
	}
	return slices.Concat(out...), size, true
}

func (m *Model) startTrace(ct radio.Contact) tea.Cmd {
	path, size, ok := tracePath(ct, m.pathHash())
	if !ok {
		m.fail("TRACE  " + ct.Name + " has no stored route; run path discovery (d) first")
		return nil
	}
	r := m.radio
	return m.radioCall(func(ctx context.Context) tea.Msg {
		tag, err := r.Trace(ctx, path, size)
		return traceStartedMsg{ct.Key, path, size, tag, err}
	})
}

// contactsEvent handles radio events that concern the contact table.
func (m *Model) contactsEvent(e radio.Event) tea.Cmd {
	c := &m.contacts
	switch e := e.(type) {
	case radio.Heard:
		if i, ok := c.heardIdx[e.Contact.Key]; ok {
			if c.heard[i].c.Name != e.Contact.Name {
				c.heardRev++
			}
			c.heard[i] = heardNode{e.Contact, m.now}
			c.arrived[e.Contact.Key] = entrance{at: m.now}
			return nil
		}
		if len(c.heard) >= heardMax {
			c.unhear(m.heardEvictee())
		}
		c.heardIdx[e.Contact.Key] = len(c.heard)
		c.heard = append(c.heard, heardNode{e.Contact, m.now})
		c.heardRev++
		c.arrived[e.Contact.Key] = entrance{at: m.now, slide: true}
		m.pruneArrived()
	case radio.ContactRemoved:
		if m.chat != nil {
			m.chat.RemoveContact(e.Key)
		}
	case radio.ContactsFull:
		c.full = true
		m.addLog(m.now, "CONTACT TABLE FULL  new nodes can't be added; prune with P on the Contacts screen", radio.ToneWarn)
	case radio.TraceResult:
		run, ok := c.traces[e.Tag]
		if !ok {
			return nil
		}
		res := e
		run.result, run.at = &res, m.now
		m.addLog(m.now, "TRACE  "+m.contactName(run.key)+"  "+traceSummary(res), radio.ToneOK)
	case radio.PathFound:
		if m.chat == nil {
			return nil
		}
		ct, ok := m.chat.ContactByPrefix(e.Prefix)
		if !ok {
			return nil
		}
		delete(c.discovering, ct.Key)
		c.found[ct.Key] = e
		m.addLog(m.now, fmt.Sprintf("ROUTE FOUND  %s  out %s · back %s", ct.Name, orFlood(e.Out, e.OutSize), orFlood(e.In, e.InSize)), radio.ToneOK)
		return m.syncContacts()
	}
	return nil
}

func orFlood(b []byte, size int) string {
	if len(b) == 0 {
		return "in range"
	}
	return hashes(b, size)
}

func traceSummary(r radio.TraceResult) string {
	parts := []string{}
	for i, h := range splitPath(r.Hashes, max(r.HashSize, 1), true) {
		if i < len(r.SNRs) {
			parts = append(parts, fmt.Sprintf("%X %+.1f", h, r.SNRs[i]))
		}
	}
	return strings.Join(parts, " · ") + fmt.Sprintf(" · back %+.1f", r.LastSNR)
}

func (m *Model) contactName(k [32]byte) string {
	if m.chat != nil {
		for _, c := range m.chat.Contacts() {
			if c.Key == k {
				return c.Name
			}
		}
	}
	return radio.KeyHex(k[:6])
}

// contactsMsg handles the results of radio calls made from the Contacts screen.
func (m *Model) contactsMsg(msg tea.Msg) (tea.Cmd, bool) {
	c := &m.contacts
	switch msg := msg.(type) {
	case contactsSyncedMsg:
		if msg.err != nil {
			m.fail("CONTACT SYNC  " + radio.Explain(msg.err))
		} else if m.chat != nil {
			m.chat.ReplaceContacts(msg.list)
			m.refreshChats()
		}
	case advertPathMsg:
		if msg.err == nil {
			c.advert[msg.key] = msg.ap
		}
	case traceStartedMsg:
		if msg.err != nil {
			m.fail("TRACE  " + radio.Explain(msg.err))
			return nil, true
		}
		c.traces[msg.tag] = &traceRun{key: msg.key, path: msg.path, size: msg.size, started: m.now}
		c.lastTrace[msg.key] = msg.tag
		m.addLog(m.now, "TRACE  "+m.contactName(msg.key)+" via "+hashes(msg.path, msg.size), radio.ToneInfo)
		tag := msg.tag
		return tea.Tick(traceTimeout, func(time.Time) tea.Msg { return traceTimeoutMsg{tag} }), true
	case traceTimeoutMsg:
		if run, ok := c.traces[msg.tag]; ok && run.result == nil {
			run.lost = true
			m.fail("TRACE  no answer from " + m.contactName(run.key) + " after " + traceTimeout.String())
		}
	case exportedMsg:
		if msg.err != nil {
			c.overlay = noOverlay
			m.fail("SHARE  " + radio.Explain(msg.err))
		} else {
			c.link = msg.link
		}
	case autoAddMsg:
		if msg.err != nil {
			c.overlay = noOverlay
			m.fail("AUTO-ADD  " + radio.Explain(msg.err))
		} else {
			c.auto, c.autoReady = msg.auto, true
		}
	case opDoneMsg:
		if msg.err != nil {
			m.fail(msg.what + "  " + radio.Explain(msg.err))
			return nil, true
		}
		m.addLog(m.now, msg.what, radio.ToneOK)
		return m.syncContacts(), true
	default:
		return nil, false
	}
	return nil, true
}

// contactsKey handles keys on the Contacts screen; unhandled keys fall through to the app.
func (m *Model) contactsKey(s string) (tea.Cmd, bool) {
	c := &m.contacts
	if c.overlay != noOverlay {
		return m.overlayKey(s), true
	}
	if c.filtering {
		filterKey(&c.filter, &c.filtering, s)
		c.sel = 0
		return nil, true
	}
	if c.heardView {
		return m.heardKey(s)
	}
	ct, have := m.selectedContact()
	switch s {
	case "up", "k":
		c.sel = max(c.sel-1, 0)
		return m.onSelectContact(), true
	case "down", "j":
		c.sel++
		return m.onSelectContact(), true
	case "/":
		c.filtering, c.filter = true, lineEdit{}
	case "esc":
		if c.filter.text == "" {
			return nil, false
		}
		c.filter = lineEdit{}
	case "F":
		c.typeFilter = (c.typeFilter + 1) % len(typeFilters)
		c.sel = 0
	case "o":
		c.sortBy = (c.sortBy + 1) % len(sortNames)
	case "a":
		c.heardView, c.heardSel = true, 0
	case "A":
		c.overlay, c.autoReady, c.autoSel = autoOverlay, false, 0
		r := m.radio
		return m.radioCall(func(ctx context.Context) tea.Msg {
			a, err := r.AutoAdd(ctx)
			return autoAddMsg{a, err}
		}), true
	case "I":
		c.overlay, c.importIn = importOverlay, lineEdit{}
	case "P":
		m.openPrune()
	default:
		if !have {
			return nil, false
		}
		return m.contactAction(s, ct)
	}
	return nil, true
}

func (m *Model) contactAction(s string, ct radio.Contact) (tea.Cmd, bool) {
	c := &m.contacts
	switch s {
	case "M":
		m.showContactPath(ct)
		return nil, true
	case "K":
		if p, ok := m.pinOf(ct.Name); ok && p.Key == ct.Key {
			m.unpin(ct.Name)
		} else {
			m.pin(ct.Name, ct.Key)
		}
		return nil, true
	case "enter":
		if ct.Type == radio.TypeRepeater {
			return m.openInAdmin(ct), true
		}
		return m.contactAction("m", ct)
	case "m":
		if ct.Type != radio.TypeChat && ct.Type != radio.TypeRoom {
			m.fail("MESSAGE  " + ct.Name + " is a " + strings.ToLower(ct.Type.String()) + "; only chat nodes and rooms take messages")
			return nil, true
		}
		m.jumpToConvo(store.ContactConvo(ct.Key))
	case "t":
		return m.startTrace(ct), true
	case "d":
		c.discovering[ct.Key] = m.now
		delete(c.found, ct.Key)
		return m.contactOp("DISCOVERING ROUTE  "+ct.Name, func(ctx context.Context, r radio.Radio) error {
			return r.DiscoverPath(ctx, ct.Key)
		}), true
	case "r":
		return m.contactOp("ROUTE RESET  "+ct.Name+" will flood until a new route is learned", func(ctx context.Context, r radio.Radio) error {
			return r.ResetPath(ctx, ct.Key)
		}), true
	case "f":
		updated := ct
		updated.Flags ^= radio.FlagFavourite
		what := "FAVOURITE  " + ct.Name
		if updated.Flags&radio.FlagFavourite == 0 {
			what = "UNFAVOURITED  " + ct.Name
		}
		return m.contactOp(what, func(ctx context.Context, r radio.Radio) error { return r.SaveContact(ctx, updated) }), true
	case "p":
		c.overlay, c.editPath, c.editSize, c.editSel = editorOverlay, nil, m.pathHash(), 0
		if ct.PathLen > 0 {
			c.editPath, c.editSize = m.rehop(ct.Path, max(ct.HashSize, 1), c.editSize)
		}
	case "e":
		c.overlay, c.link = shareOverlay, ""
		r := m.radio
		return m.radioCall(func(ctx context.Context) tea.Msg {
			link, err := r.ExportContact(ctx, ct.Key)
			return exportedMsg{link, err}
		}), true
	case "l":
		c.overlay, c.placeIn = placeOverlay, lineEdit{}
		if p, ok := m.manualPlace(ct.Key); ok {
			c.placeIn.set(fmt.Sprintf("%.5f, %.5f", p.Lat, p.Lon))
		}
	case "x":
		c.overlay = removeOverlay
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) jumpToConvo(cv store.Convo) {
	m.setTab(0)
	ch := &m.chats
	ch.filter, ch.filtering, ch.onAdd = lineEdit{}, false, false
	if strings.HasPrefix(string(cv), "dm:") {
		m.openDraft(cv)
		return
	}
	for i, info := range ch.infos {
		if info.Convo == cv {
			ch.sel = i
		}
	}
	ch.scroll = 0
	m.refreshChats()
}

func (m *Model) heardKey(s string) (tea.Cmd, bool) {
	c := &m.contacts
	switch s {
	case "esc", "a":
		c.heardView = false
	case "up", "k":
		c.heardSel = max(c.heardSel-1, 0)
	case "down", "j":
		c.heardSel = min(c.heardSel+1, max(len(c.heard)-1, 0))
	case "enter":
		if c.heardSel < len(c.heard) {
			h := c.heard[c.heardSel]
			c.unhear(c.heardSel)
			return m.contactOp("ADDED  "+h.c.Name, func(ctx context.Context, r radio.Radio) error {
				return r.SaveContact(ctx, h.c)
			}), true
		}
	default:
		return nil, false
	}
	return nil, true
}

// heardMax is how many heard nodes are kept.
const heardMax = 1000

// heardStale is how long a node may go unheard before it's the first to make room.
const heardStale = 48 * time.Hour

// heardEvictee picks the heard node to forget for a new one. Keys cost nothing to make, so a flood of them
// mustn't push out the nodes the look-alike warnings know: a node gone quiet goes first, then the newest
// arrival, and a node that has signed a message or holds a pinned key only when nothing else is left.
func (m *Model) heardEvictee() int {
	c := &m.contacts
	kept := map[[32]byte]bool{}
	for _, msg := range m.chats.signedMsgs {
		if msg.Sig == store.SigValid && len(msg.SigKey) == 32 {
			kept[[32]byte(msg.SigKey)] = true
		}
	}
	for _, p := range m.pins() {
		kept[p.Key] = true
	}
	stale := -1
	for i, h := range c.heard {
		if !kept[h.c.Key] && m.now.Sub(h.at) > heardStale && (stale < 0 || h.at.Before(c.heard[stale].at)) {
			stale = i
		}
	}
	if stale >= 0 {
		return stale
	}
	for i := len(c.heard) - 1; i >= 0; i-- {
		if !kept[c.heard[i].c.Key] {
			return i
		}
	}
	return len(c.heard) - 1
}

func (c *contactsModel) unhear(i int) {
	delete(c.heardIdx, c.heard[i].c.Key)
	c.heard = slices.Delete(c.heard, i, i+1)
	c.heardRev++
	for k := i; k < len(c.heard); k++ {
		c.heardIdx[c.heard[k].c.Key] = k
	}
	if i < c.heardSel {
		c.heardSel--
	}
	c.heardSel = max(min(c.heardSel, len(c.heard)-1), 0)
}

// pruneArrived forgets entrances that have finished, once there are many.
func (m *Model) pruneArrived() {
	if len(m.contacts.arrived) <= heardMax {
		return
	}
	for k, a := range m.contacts.arrived {
		if !motion.Running(a.at, arriveRowDur, m.now) {
			delete(m.contacts.arrived, k)
		}
	}
}

func (m *Model) openPrune() {
	c := &m.contacts
	var cands []radio.Contact
	for _, ct := range m.chat.Contacts() {
		if ct.Flags&radio.FlagFavourite == 0 {
			cands = append(cands, ct)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].LastAdvert.Before(cands[j].LastAdvert) })
	c.prune = nil
	for i := 0; i < len(cands) && i < 5; i++ {
		c.prune = append(c.prune, cands[i].Key)
	}
	if len(c.prune) == 0 {
		m.fail("PRUNE  nothing to suggest: every contact is a favourite")
		return
	}
	c.overlay = pruneOverlay
}

func (m *Model) overlayKey(s string) tea.Cmd {
	c := &m.contacts
	ct, _ := m.selectedContact()
	if s == "esc" {
		c.overlay = noOverlay
		return nil
	}
	switch c.overlay {
	case shareOverlay:
		if s == "y" && c.link != "" {
			m.addLog(m.now, "COPIED  link for "+ct.Name, radio.ToneOK)
			return m.copyText(c.link)
		}
		c.overlay = noOverlay
	case removeOverlay:
		c.overlay = noOverlay
		if s == "y" {
			return m.contactOp("REMOVED  "+ct.Name, func(ctx context.Context, r radio.Radio) error { return r.RemoveContact(ctx, ct.Key) })
		}
	case placeOverlay:
		if s != "enter" {
			c.placeIn.key(s)
			return nil
		}
		c.overlay = noOverlay
		m.placeContact(ct, c.placeIn.text)
	case importOverlay:
		switch s {
		case "enter":
			link := strings.TrimSpace(c.importIn.text)
			c.overlay = noOverlay
			data, err := radio.ParseLink(link)
			if err != nil {
				m.fail("IMPORT  " + err.Error())
				return nil
			}
			c.importing, c.overlay = m.readLink(link, data), importConfirmOverlay
		default:
			c.importIn.key(s)
		}
	case importConfirmOverlay:
		c.overlay = noOverlay
		if s != "y" {
			return nil
		}
		link, what := c.importing.link, "IMPORTED  contact from link"
		if c.importing.name != "" {
			what = "IMPORTED  " + c.importing.name
		}
		return m.contactOp(what, func(ctx context.Context, r radio.Radio) error { return r.ImportContact(ctx, link) })
	case editorOverlay:
		reps := m.repeaters()
		switch s {
		case "up", "k":
			c.editSel = max(c.editSel-1, 0)
		case "down", "j":
			c.editSel = min(c.editSel+1, max(len(reps)-1, 0))
		case "enter":
			// A path is at most 64 bytes, and its length byte counts at most 63 hops.
			if c.editSel < len(reps) && len(c.editPath)+c.editSize <= 64 && len(c.editPath)/c.editSize < 63 {
				c.editPath = append(c.editPath, reps[c.editSel].Key[:c.editSize]...)
			}
		case "backspace":
			if len(c.editPath) > 0 {
				c.editPath = c.editPath[:len(c.editPath)-c.editSize]
			}
		case "s":
			c.overlay = noOverlay
			updated := ct
			updated.PathLen, updated.Path, updated.HashSize = len(c.editPath)/c.editSize, append([]byte(nil), c.editPath...), c.editSize
			what := fmt.Sprintf("ROUTE SET  %s via %s", ct.Name, orFlood(updated.Path, updated.HashSize))
			return m.contactOp(what, func(ctx context.Context, r radio.Radio) error { return r.SaveContact(ctx, updated) })
		}
	case autoOverlay:
		if !c.autoReady {
			return nil
		}
		a := &c.auto
		toggles := []*bool{&a.Chat, &a.Repeater, &a.Room, &a.Sensor, &a.OverwriteOldest}
		switch s {
		case "up", "k":
			c.autoSel = max(c.autoSel-1, 0)
		case "down", "j":
			c.autoSel = min(c.autoSel+1, len(toggles))
		case "space", "enter":
			if c.autoSel < len(toggles) {
				*toggles[c.autoSel] = !*toggles[c.autoSel]
			}
		case "left", "h", "-":
			if c.autoSel == len(toggles) {
				a.MaxHops = max(a.MaxHops-1, 0)
			}
		case "right", "l", "+", "=":
			if c.autoSel == len(toggles) {
				a.MaxHops = min(a.MaxHops+1, 64)
			}
		case "s":
			c.overlay = noOverlay
			set := *a
			return m.contactOp("AUTO-ADD SAVED", func(ctx context.Context, r radio.Radio) error { return r.SetAutoAdd(ctx, set) })
		}
	case pruneOverlay:
		c.overlay = noOverlay
		if s == "y" {
			keys := append([][32]byte(nil), c.prune...)
			c.full = false
			return m.contactOp(fmt.Sprintf("PRUNED  %d contacts", len(keys)), func(ctx context.Context, r radio.Radio) error {
				for _, k := range keys {
					if err := r.RemoveContact(ctx, k); err != nil {
						return err
					}
				}
				return nil
			})
		}
	}
	return nil
}

// rehop recuts a stored route's hops to another size. A hop is lengthened only when one repeater's key starts
// with it; if any can't be, the route keeps the size it has.
func (m *Model) rehop(path []byte, from, to int) ([]byte, int) {
	var out []byte
	for h := range slices.Chunk(path, from) {
		if to <= len(h) {
			out = append(out, h[:to]...)
			continue
		}
		var match []radio.Contact
		for _, r := range m.repeaters() {
			if bytes.HasPrefix(r.Key[:], h) {
				match = append(match, r)
			}
		}
		if len(match) != 1 {
			return append([]byte(nil), path...), from
		}
		out = append(out, match[0].Key[:to]...)
	}
	return out, to
}

func (m *Model) repeaters() []radio.Contact {
	var out []radio.Contact
	for _, ct := range m.chat.Contacts() {
		if ct.Type == radio.TypeRepeater {
			out = append(out, ct)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (m *Model) contactsAnimating() bool {
	for _, run := range m.contacts.traces {
		if run.result == nil && !run.lost || run.result != nil && motion.Running(run.at, traceReveal, m.now) {
			return true
		}
	}
	for _, a := range m.contacts.arrived {
		if motion.Running(a.at, arriveRowDur, m.now) {
			return m.visible(1)
		}
	}
	return len(m.contacts.discovering) > 0
}

const arriveRowDur = 600 * time.Millisecond

// entrance is when a row came: a new one slides in, one freshly advertised only lights up.
type entrance struct {
	at    time.Time
	slide bool
}

// arriving draws a row that's just come as plain text on a raised background fading away, sliding in
// from the right first if it's new.
func (m *Model) arriving(row string, w int, a entrance) string {
	th := m.th
	if !m.level.Allows(motion.Flash) || !motion.Running(a.at, arriveRowDur, m.now) {
		return row
	}
	p := motion.Progress(a.at, arriveRowDur, m.now)
	text := fit(ansi.Strip(row), w)
	if a.slide && m.level.Allows(motion.Wipe) {
		in := motion.EaseOut(min(p/0.4, 1))
		text = fit(strings.Repeat(" ", int((1-in)*float64(w)+0.5))+text, w)
	}
	return th.Block(th.Text, th.Raised.Lerp(th.Bg, motion.EaseOut(p))).Bold(false).Render(text)
}

// placeContact puts a node on the map by hand from "lat, lon", or takes it off again when in is empty.
func (m *Model) placeContact(ct radio.Contact, in string) {
	key := radio.KeyHex(ct.Key[:])
	if strings.TrimSpace(in) == "" {
		if _, ok := m.manualPlace(ct.Key); ok {
			m.adminConfig().SetLocation(key, 0, 0, false)
			m.saveLocations()
			m.addLog(m.now, "LOCATION  "+ct.Name+" back to what it shares", radio.ToneInfo)
		}
		return
	}
	a, b, ok := strings.Cut(in, ",")
	lat, err := parseFloat(a, "latitude", -90, 90)
	if err == nil {
		var lon float64
		if lon, err = parseFloat(b, "longitude", -180, 180); err == nil && ok {
			m.adminConfig().SetLocation(key, lat, lon, true)
			m.saveLocations()
			m.addLog(m.now, fmt.Sprintf("LOCATION  %s placed at %.5f, %.5f", ct.Name, lat, lon), radio.ToneOK)
			return
		}
	}
	if !ok {
		err = errors.New("a location is latitude, longitude, e.g. 51.4545, -2.5879")
	}
	m.fail("LOCATION  " + err.Error())
}

func (m *Model) saveLocations() {
	m.mapv.cacheKey = ""
	if m.opt.Config != nil {
		m.saveConfig()
	}
}

// linkContact is who a contact link says it's for, read before anything goes to the radio.
type linkContact struct {
	link  string
	name  string
	key   []byte
	clash string // a contact already called that, with another key
}

// readLink reads the advert in a link: the radio exports a whole packet, other apps sometimes the bare advert.
func (m *Model) readLink(link string, data []byte) linkContact {
	lc := linkContact{link: link}
	advert := data
	if p, err := meshcore.PacketFromBytes(data); err == nil && p.PayloadType() == meshcore.PayloadTypeAdvert {
		advert = p.Payload
	}
	a, err := meshcore.AdvertFromBytes(advert)
	if err != nil || a.AppDataErr() != nil {
		return lc
	}
	key := a.PublicKey.PublicKey()
	lc.name, lc.key = untrusted.Text(a.AppData().Name), key[:]
	for _, ct := range m.chat.Contacts() {
		if strings.EqualFold(ct.Name, lc.name) && ct.Key != key {
			lc.clash = ct.Name
		}
	}
	return lc
}

package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
	"meshcore-tui/internal/theme"
)

const (
	opTimeout    = 15 * time.Second
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
	started time.Time
	result  *radio.TraceResult
	at      time.Time // when the result arrived, for the hop-by-hop reveal
	lost    bool
}

type contactsModel struct {
	sel        int
	typeFilter int
	sortBy     int
	filter     string
	filtering  bool
	heardView  bool
	heard      []heardNode
	heardSel   int
	full       bool

	advert      map[[32]byte]radio.AdvertPath
	traces      map[uint32]*traceRun
	lastTrace   map[[32]byte]uint32
	discovering map[[32]byte]time.Time
	found       map[[32]byte]radio.PathFound

	overlay   overlay
	link      string
	editPath  []byte
	editSel   int
	importIn  string
	placeIn   lineEdit
	auto      radio.AutoAdd
	autoSel   int
	autoReady bool
	prune     [][32]byte
}

func newContacts() contactsModel {
	return contactsModel{advert: map[[32]byte]radio.AdvertPath{}, traces: map[uint32]*traceRun{},
		lastTrace: map[[32]byte]uint32{}, discovering: map[[32]byte]time.Time{}, found: map[[32]byte]radio.PathFound{}}
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
	r := m.radio
	if r == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		return f(ctx)
	}
}

func (m *Model) syncContacts() tea.Cmd {
	r := m.radio
	return m.radioCall(func(ctx context.Context) tea.Msg {
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

func hashes(b []byte) string {
	parts := make([]string, len(b))
	for i, h := range b {
		parts[i] = fmt.Sprintf("%02X", h)
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
	return fmt.Sprintf("direct %s", hashes(c.Path))
}

// rows is the contact table after the type filter, text filter and sort.
func (m *Model) contactRows() []radio.Contact {
	if m.chat == nil {
		return nil
	}
	c := &m.contacts
	var out []radio.Contact
	want, favOnly := typeFilters[c.typeFilter].typ, typeFilters[c.typeFilter].fav
	q := strings.ToLower(c.filter)
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
// A repeater with no stored route is traced as a direct neighbour.
func tracePath(ct radio.Contact) ([]byte, bool) {
	var out []byte
	switch {
	case ct.PathLen > 0:
		out = append(out, ct.Path...)
		if ct.Type == radio.TypeRepeater {
			out = append(out, ct.Key[0])
		}
	case ct.Type == radio.TypeRepeater:
		out = []byte{ct.Key[0]}
	default:
		return nil, false
	}
	back := append([]byte(nil), out...)
	for i := len(back) - 2; i >= 0; i-- {
		out = append(out, back[i])
	}
	return out, true
}

func (m *Model) startTrace(ct radio.Contact) tea.Cmd {
	path, ok := tracePath(ct)
	if !ok {
		m.fail("TRACE  " + ct.Name + " has no stored route; run path discovery (d) first")
		return nil
	}
	r := m.radio
	return m.radioCall(func(ctx context.Context) tea.Msg {
		tag, err := r.Trace(ctx, path)
		return traceStartedMsg{ct.Key, path, tag, err}
	})
}

// contactsEvent handles radio events that concern the contact table.
func (m *Model) contactsEvent(e radio.Event) tea.Cmd {
	c := &m.contacts
	switch e := e.(type) {
	case radio.Heard:
		for i, h := range c.heard {
			if h.c.Key == e.Contact.Key {
				c.heard[i] = heardNode{e.Contact, m.now}
				return nil
			}
		}
		c.heard = append(c.heard, heardNode{e.Contact, m.now})
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
		m.addLog(m.now, fmt.Sprintf("ROUTE FOUND  %s  out %s · back %s", ct.Name, orFlood(e.Out), orFlood(e.In)), radio.ToneOK)
		return m.syncContacts()
	}
	return nil
}

func orFlood(b []byte) string {
	if len(b) == 0 {
		return "in range"
	}
	return hashes(b)
}

func traceSummary(r radio.TraceResult) string {
	parts := []string{}
	for i, h := range r.Hashes {
		if i < len(r.SNRs) {
			parts = append(parts, fmt.Sprintf("%02X %+.1f", h, r.SNRs[i]))
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
		c.traces[msg.tag] = &traceRun{key: msg.key, path: msg.path, started: m.now}
		c.lastTrace[msg.key] = msg.tag
		m.addLog(m.now, "TRACE  "+m.contactName(msg.key)+" via "+hashes(msg.path), radio.ToneInfo)
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
		switch s {
		case "esc":
			c.filter, c.filtering = "", false
		case "enter":
			c.filtering = false
		case "backspace":
			c.filter = dropLast(c.filter)
		default:
			if len([]rune(s)) == 1 {
				c.filter += s
			}
		}
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
		c.filtering, c.filter = true, ""
	case "esc":
		if c.filter == "" {
			return nil, false
		}
		c.filter = ""
	case "F":
		c.typeFilter = (c.typeFilter + 1) % len(typeFilters)
		c.sel = 0
	case "s":
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
		c.overlay, c.importIn = importOverlay, ""
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
		if p, ok := m.pins()[ct.Name]; ok && p.Key == ct.Key {
			m.unpin(ct.Name)
		} else {
			m.pin(ct.Name, ct.Key)
		}
		return nil, true
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
		c.overlay, c.editPath, c.editSel = editorOverlay, nil, 0
		if ct.PathLen > 0 {
			c.editPath = append([]byte(nil), ct.Path...)
		}
	case "e":
		c.overlay, c.link = shareOverlay, ""
		r := m.radio
		return m.radioCall(func(ctx context.Context) tea.Msg {
			link, err := r.ExportContact(ctx, ct.Key)
			return exportedMsg{link, err}
		}), true
	case "L":
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
	ch.filter, ch.filtering = "", false
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
			c.heard = append(c.heard[:c.heardSel], c.heard[c.heardSel+1:]...)
			c.heardSel = max(min(c.heardSel, len(c.heard)-1), 0)
			return m.contactOp("ADDED  "+h.c.Name, func(ctx context.Context, r radio.Radio) error {
				return r.SaveContact(ctx, h.c)
			}), true
		}
	default:
		return nil, false
	}
	return nil, true
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
		if s == "c" && c.link != "" {
			m.addLog(m.now, "COPIED  link for "+ct.Name, radio.ToneOK)
			return tea.SetClipboard(c.link)
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
			link := strings.TrimSpace(c.importIn)
			c.overlay = noOverlay
			if _, err := radio.ParseLink(link); err != nil {
				m.fail("IMPORT  " + err.Error())
				return nil
			}
			return m.contactOp("IMPORTED  contact from link", func(ctx context.Context, r radio.Radio) error { return r.ImportContact(ctx, link) })
		case "backspace":
			c.importIn = dropLast(c.importIn)
		default:
			if len([]rune(s)) == 1 {
				c.importIn += s
			}
		}
	case editorOverlay:
		reps := m.repeaters()
		switch s {
		case "up", "k":
			c.editSel = max(c.editSel-1, 0)
		case "down", "j":
			c.editSel = min(c.editSel+1, max(len(reps)-1, 0))
		case "enter":
			if c.editSel < len(reps) && len(c.editPath) < 64 {
				c.editPath = append(c.editPath, reps[c.editSel].Key[0])
			}
		case "backspace":
			if len(c.editPath) > 0 {
				c.editPath = c.editPath[:len(c.editPath)-1]
			}
		case "s":
			c.overlay = noOverlay
			updated := ct
			updated.PathLen, updated.Path = len(c.editPath), append([]byte(nil), c.editPath...)
			what := fmt.Sprintf("ROUTE SET  %s via %s", ct.Name, orFlood(updated.Path))
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
	return len(m.contacts.discovering) > 0
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

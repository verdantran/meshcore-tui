package radio

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A companion's boot details and contact names are drawn on one row each; none may carry a control or line break.
func TestBootDetailsAreCleaned(t *testing.T) {
	f := newFakeDevice()
	f.self.Name = "k4\x90+q\x1b\\\r\nFAKE  ✓"
	f.contacts[0].AdvertName = "Eve\n10:00:00  SIGNED ✓ Alice"
	l := startLink(t, f)
	var before []Event
	ready := next[Ready](t, l, &before)
	bad := func(s string) bool {
		return !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r >= 0x7f && r <= 0x9f })
	}
	for _, e := range before {
		if s, ok := e.(BootStep); ok && bad(s.Detail) {
			t.Errorf("boot step %s detail %q", s.Key, s.Detail)
		}
	}
	if bad(ready.Self.Name) || bad(ready.ContactList[0].Name) {
		t.Errorf("self %q, contact %q", ready.Self.Name, ready.ContactList[0].Name)
	}
}

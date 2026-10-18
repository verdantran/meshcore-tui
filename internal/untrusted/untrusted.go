// Package untrusted makes text from outside the app (the mesh, a repeater's console, downloaded map tiles)
// safe to draw: a terminal acts on control characters, so none may reach it from someone else.
package untrusted

import "strings"

// Text drops control characters (C0 but line breaks, DEL and C1, so escape sequences can't form) and the
// bidirectional overrides that can make text read in a different order to how it's stored. Tabs become
// spaces, and invalid UTF-8 becomes U+FFFD.
func Text(s string) string {
	clean := true
	for _, r := range s {
		if drop(r) || r == '\t' || r == '�' {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case drop(r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}

func drop(r rune) bool {
	switch {
	case r == '\n':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

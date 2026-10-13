package notify

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetect(t *testing.T) {
	cases := []struct {
		vars map[string]string
		want Method
	}{
		{map[string]string{"TERM_PROGRAM": "ghostty"}, Terminal},
		{map[string]string{"TERM": "xterm-kitty"}, Terminal},
		{map[string]string{"TERM_PROGRAM": "ghostty", "SSH_CONNECTION": "1 2 3 4"}, Terminal},
		{map[string]string{"TERM": "xterm-256color", "SSH_CONNECTION": "1 2 3 4"}, BellOnly},
	}
	for _, c := range cases {
		if got := Detect(env(c.vars)); got != c.want {
			t.Errorf("Detect(%v) = %s, want %s", c.vars, got, c.want)
		}
	}
	if got := Resolve("off", nil); got != Off {
		t.Errorf("Resolve(off) = %s", got)
	}
}

func TestEscape(t *testing.T) {
	ghostty := env(map[string]string{"TERM_PROGRAM": "ghostty"})
	if got := Escape(Terminal, "Alice", "hi\x1b]evil", true, ghostty); got != "\a\x1b]9;Alice: hi ]evil\a" {
		t.Errorf("OSC 9 = %q", got)
	}
	kitty := env(map[string]string{"TERM": "xterm-kitty"})
	if got := Escape(Terminal, "Alice", "hi", false, kitty); got != "\x1b]99;i=mc:d=0;Alice\x1b\\\x1b]99;i=mc:d=1:p=body;hi\x1b\\" {
		t.Errorf("OSC 99 = %q", got)
	}
	if got := Escape(OS, "a", "b", true, ghostty); got != "\a" {
		t.Errorf("OS method should only ring the bell in the terminal, got %q", got)
	}
	if got := Escape(Off, "a", "b", true, ghostty); got != "" {
		t.Errorf("off should be silent, got %q", got)
	}
}

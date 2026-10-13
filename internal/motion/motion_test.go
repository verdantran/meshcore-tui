package motion

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)

func TestTypewriter(t *testing.T) {
	cases := []struct {
		after time.Duration
		want  string
		done  bool
	}{
		{0, "", false},
		{100 * time.Millisecond, "LIN", false},
		{time.Second, "LINK [ OK ]", true},
	}
	for _, c := range cases {
		got, done := Typewriter("LINK [ OK ]", t0, 30, t0.Add(c.after))
		if got != c.want || done != c.done {
			t.Errorf("after %v: got %q,%v want %q,%v", c.after, got, done, c.want, c.done)
		}
	}
}

func TestDecodeIsDeterministicAndEndsOnText(t *testing.T) {
	a := DecodeText("hello mesh", 42, 0.3)
	b := DecodeText("hello mesh", 42, 0.3)
	if a != b {
		t.Fatalf("same seed and time gave %q and %q", a, b)
	}
	if got := DecodeText("hello mesh", 42, 1); got != "hello mesh" {
		t.Fatalf("finished decode = %q", got)
	}
	if len([]rune(a)) != len([]rune("hello mesh")) {
		t.Fatalf("decode changed length: %q", a)
	}
}

func TestLevels(t *testing.T) {
	if !Reduced.Allows(Flash) || Reduced.Allows(Glitch) || Reduced.Allows(Ambient) {
		t.Fatal("reduced should keep flashes and drop glitches and ambient effects")
	}
	if Off.Allows(Flash) {
		t.Fatal("off should allow nothing")
	}
}

func TestCount(t *testing.T) {
	if got := Count(0, 10, t0, time.Second, t0.Add(2*time.Second)); got != 10 {
		t.Fatalf("count after end = %v", got)
	}
	if got := Count(0, 10, t0, time.Second, t0); got != 0 {
		t.Fatalf("count at start = %v", got)
	}
}

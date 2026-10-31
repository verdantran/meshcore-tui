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

func TestHelpers(t *testing.T) {
	for _, l := range []Level{Full, Reduced, Off} {
		if back, ok := ParseLevel(l.String()); !ok || back != l {
			t.Errorf("round trip %s", l)
		}
	}
	if _, ok := ParseLevel("lots"); ok {
		t.Error("unknown level parsed")
	}
	if Progress(t0, 0, t0) != 1 || Progress(time.Time{}, time.Second, t0) != 1 {
		t.Error("no duration, or no start, means finished")
	}
	if Running(time.Time{}, time.Second, t0) || !Running(t0, time.Second, t0.Add(500*time.Millisecond)) || Running(t0, time.Second, t0.Add(-time.Millisecond)) {
		t.Error("running window")
	}
	if got, done := Typewriter("abc", time.Time{}, 10, t0); got != "abc" || !done {
		t.Error("an unstarted typewriter shows everything")
	}
	if TypeDur("abcd", 2) != 2*time.Second {
		t.Error("type duration")
	}
	g := GlitchText("LINK FAILED", 5, 1)
	if g == "LINK FAILED" || GlitchText("LINK FAILED", 5, 1) != g {
		t.Errorf("glitch should change text, deterministically: %q", g)
	}
	if len([]rune(RampBar(t0, 3))) != 3 || RampRune(t0) == 0 {
		t.Error("ramp")
	}
	if !Full.Allows(Glitch) || Reduced.Allows(Decode) || !Reduced.Allows(Typing) {
		t.Error("allows")
	}
}

func TestMarqueeHoldsScrollsAndHolds(t *testing.T) {
	t0 := time.Unix(0, 0)
	at := func(d time.Duration) int { return Marquee(16, t0, t0.Add(d)) }
	for _, c := range []struct {
		d    time.Duration
		want int
	}{{0, 0}, {time.Second, 0}, {marqueeHold + time.Second, 8}, {marqueeHold + 2*time.Second, 16}, {2*marqueeHold + 2*time.Second - time.Millisecond, 16}, {2*marqueeHold + 2*time.Second, 0}} {
		if got := at(c.d); got != c.want {
			t.Errorf("after %v: %d, want %d", c.d, got, c.want)
		}
	}
	if Marquee(0, t0, t0.Add(time.Hour)) != 0 || Marquee(5, t0, t0.Add(-time.Second)) != 0 {
		t.Error("nothing to scroll, or not started")
	}
}

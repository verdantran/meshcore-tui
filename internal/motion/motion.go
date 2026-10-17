// Package motion implements the effects in SPEC.md §6.2 as pure functions of time,
// so every frame is deterministic and testable with a fake clock.
package motion

import (
	"math"
	"math/rand"
	"os"
	"time"
)

type Level int

const (
	Full Level = iota
	Reduced
	Off
)

func (l Level) String() string { return [...]string{"full", "reduced", "off"}[l] }

func ParseLevel(s string) (Level, bool) {
	l, ok := map[string]Level{"full": Full, "reduced": Reduced, "off": Off}[s]
	return l, ok
}

// DefaultLevel is full locally and reduced over SSH, where constant redraws make the UI lag.
func DefaultLevel() Level {
	if os.Getenv("SSH_CONNECTION") != "" {
		return Reduced
	}
	return Full
}

type Effect int

const (
	Flash   Effect = iota // lamps, highlights
	CountUp               // numbers rolling to a new value
	Ramp                  // progress and waiting indicators
	Typing                // typewriter reveals
	Decode                // scrambled text locking in
	Wipe                  // screen transitions, header slide-in
	Glitch                // failure jitter
	Ambient               // ticker scroll and other idle effects
)

// Allows reports whether an effect runs at this level; reduced keeps state feedback and drops flourish.
func (l Level) Allows(e Effect) bool {
	switch l {
	case Off:
		return false
	case Reduced:
		return e == Flash || e == CountUp || e == Ramp || e == Typing
	}
	return true
}

const FrameRate = 30

var FrameDur = time.Second / FrameRate

// Progress is how far an effect started at start has run, clamped to [0, 1].
func Progress(start time.Time, dur time.Duration, now time.Time) float64 {
	if dur <= 0 || start.IsZero() {
		return 1
	}
	return min(max(float64(now.Sub(start))/float64(dur), 0), 1)
}

func Running(start time.Time, dur time.Duration, now time.Time) bool {
	return !start.IsZero() && now.Sub(start) >= 0 && now.Sub(start) < dur
}

func EaseOut(t float64) float64 { return 1 - math.Pow(1-t, 3) }

// Pulse rises from 0 to 1 and back once a period, smoothly, for something waiting on you.
func Pulse(now time.Time, period time.Duration) float64 {
	return 0.5 - 0.5*math.Cos(2*math.Pi*float64(now.UnixNano()%int64(period))/float64(period))
}

// Typewriter returns the prefix of text revealed after typing at cps characters a second.
func Typewriter(text string, start time.Time, cps float64, now time.Time) (string, bool) {
	r := []rune(text)
	if start.IsZero() {
		return text, true
	}
	n := int(now.Sub(start).Seconds() * cps)
	if n >= len(r) {
		return text, true
	}
	return string(r[:max(n, 0)]), false
}

func TypeDur(text string, cps float64) time.Duration {
	return time.Duration(float64(len([]rune(text))) / cps * float64(time.Second))
}

const scramble = "0123456789ABCDEF░▒▓█▚▞▙▟┼╳╱╲<>/\\#*+=%$"

// DecodeText shows text scrambled into hex and block glyphs, locking in left to right as t goes 0→1.
func DecodeText(text string, seed int64, t float64) string {
	r := []rune(text)
	if t >= 1 {
		return text
	}
	glyphs := []rune(scramble)
	locked := int(EaseOut(t) * float64(len(r)))
	rng := rand.New(rand.NewSource(seed + int64(t*1000)))
	out := make([]rune, len(r))
	for i, c := range r {
		if i < locked || c == ' ' {
			out[i] = c
			continue
		}
		out[i] = glyphs[rng.Intn(len(glyphs))]
	}
	return string(out)
}

// GlitchText scrambles a few characters and shifts the line by up to one cell, for a failure jolt.
func GlitchText(text string, seed int64, frame int) string {
	rng := rand.New(rand.NewSource(seed + int64(frame)*7919))
	r := []rune(text)
	glyphs := []rune(scramble)
	for i := range r {
		if r[i] != ' ' && rng.Float64() < 0.18 {
			r[i] = glyphs[rng.Intn(len(glyphs))]
		}
	}
	if rng.Intn(2) == 0 {
		return " " + string(r)
	}
	return string(r)
}

// Count rolls a number from one value to another with ease-out.
func Count(from, to float64, start time.Time, dur time.Duration, now time.Time) float64 {
	return from + (to-from)*EaseOut(Progress(start, dur, now))
}

var rampRunes = []rune("▁▂▃▄▅▆▇█▇▆▅▄▃▂")

// RampRune is a block-ramp spinner frame.
func RampRune(now time.Time) rune {
	return rampRunes[int(now.UnixMilli()/70)%len(rampRunes)]
}

// RampBar is a short animated block ramp, used beside pending work.
func RampBar(now time.Time, n int) string {
	out := make([]rune, n)
	base := int(now.UnixMilli() / 70)
	for i := range out {
		out[i] = rampRunes[(base+i*2)%len(rampRunes)]
	}
	return string(out)
}

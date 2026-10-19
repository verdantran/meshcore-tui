//go:build race

package ui

// timed is false under the race detector, which slows code too much for timings to mean anything.
const timed = false

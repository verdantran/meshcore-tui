package main

import (
	"os"
	"sync/atomic"
	"time"
)

const (
	slowWrite  = 30 * time.Millisecond
	slowWrites = 3 // in a row, so one hiccup isn't enough
)

// timedOutput is stdout, noticing when frames take too long to write, as they do over a slow
// SSH link. Embedding the file keeps it a terminal to Bubble Tea.
type timedOutput struct {
	*os.File
	slow   func()
	streak int
	told   atomic.Bool
}

func (o *timedOutput) Write(b []byte) (int, error) {
	start := time.Now()
	n, err := o.File.Write(b)
	if time.Since(start) < slowWrite {
		o.streak = 0
	} else if o.streak++; o.streak >= slowWrites && o.slow != nil && !o.told.Swap(true) {
		go o.slow()
	}
	return n, err
}

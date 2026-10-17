package main

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestTimedOutputNoticesSlowWrites(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var told atomic.Int32
	out := &timedOutput{File: w, slow: func() { told.Add(1) }}
	out.Write([]byte("quick"))
	go func() {
		buf := make([]byte, 1<<20)
		for {
			time.Sleep(2 * slowWrite)
			if _, err := r.Read(buf); err != nil {
				return
			}
		}
	}()
	big := make([]byte, 1<<17) // more than a pipe holds, so each write waits on the reader
	for range slowWrites + 2 {
		out.Write(big)
	}
	w.Close()
	time.Sleep(10 * time.Millisecond)
	if told.Load() != 1 {
		t.Errorf("told %d times, want once", told.Load())
	}
}

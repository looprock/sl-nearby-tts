package main

// The speech side: a small interface with one implementation per platform, plus
// a queue that keeps a busy region from backing speech up by minutes.

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Speaker turns text into audio. Speak blocks until the line has been spoken,
// which is what gives the queue its natural pacing.
type Speaker interface {
	Speak(text string) error
	Close() error
}

// SpeechOpts are passed through to the platform backend. Rate and Volume are
// deliberately strings because their scales differ per platform; see the README.
type SpeechOpts struct {
	Voice  string
	Rate   string
	Volume string
	Logf   func(format string, args ...any)
}

func (o SpeechOpts) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// dryRunSpeaker prints what would be spoken. It is the default on a machine
// with no speech backend and what -dry-run selects everywhere.
type dryRunSpeaker struct {
	w  io.Writer
	mu sync.Mutex
}

func (d *dryRunSpeaker) Speak(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := fmt.Fprintf(d.w, "SPEAK: %s\n", text)
	return err
}

func (d *dryRunSpeaker) Close() error { return nil }

// Queue serializes utterances onto a single Speaker, dropping the oldest
// pending lines when it cannot keep up. Falling behind on a crowded parcel is
// normal; being minutes behind is useless, so staying current beats being
// complete.
type Queue struct {
	ch      chan string
	sp      Speaker
	logf    func(format string, args ...any)
	dropped atomic.Int64
	wg      sync.WaitGroup
	closed  atomic.Bool
}

func NewQueue(sp Speaker, depth int, logf func(string, ...any)) *Queue {
	if depth < 1 {
		depth = 1
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	q := &Queue{ch: make(chan string, depth), sp: sp, logf: logf}
	q.wg.Add(1)
	go q.run()
	return q
}

func (q *Queue) run() {
	defer q.wg.Done()
	for text := range q.ch {
		if err := q.sp.Speak(text); err != nil {
			q.logf("speak failed: %v", err)
		}
	}
}

// Offer enqueues a line without ever blocking the reader goroutine.
func (q *Queue) Offer(text string) {
	if q.closed.Load() {
		return
	}
	for {
		select {
		case q.ch <- text:
			return
		default:
		}
		// Full: discard the stalest line and try again. The loop re-tries
		// rather than assuming the drain succeeded, since the speaking
		// goroutine may have taken the slot in between.
		select {
		case <-q.ch:
			n := q.dropped.Add(1)
			if n == 1 || n%10 == 0 {
				q.logf("speech queue full, dropped %d line(s) so far", n)
			}
		default:
		}
	}
}

// Dropped reports how many lines were discarded for being too far behind.
func (q *Queue) Dropped() int64 { return q.dropped.Load() }

// Close stops accepting lines, waits for the backlog to be spoken, and shuts
// the backend down.
func (q *Queue) Close() error {
	if q.closed.Swap(true) {
		return nil
	}
	close(q.ch)
	q.wg.Wait()
	return q.sp.Close()
}

// Drop discards lines that are still waiting. The line being spoken right now
// belongs to the backend and cannot be recalled, so "tts off" goes quiet after
// at most one more utterance. These are deliberate discards, so they are not
// counted as falling behind.
func (q *Queue) Drop() {
	// After Close the channel is closed, and every receive on it would succeed
	// immediately -- the default case would never be reached.
	if q.closed.Load() {
		return
	}
	for {
		select {
		case <-q.ch:
		default:
			return
		}
	}
}

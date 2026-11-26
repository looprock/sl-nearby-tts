package main

// sl-nearby-tts reads Second Life nearby chat out of a running viewer and
// speaks it.
//
// The viewer launches it as a LEAP plugin:
//
//	SecondLife --leap "'C:/Tools/sl-nearby-tts.exe'"
//
// Both the inner single quotes and the forward slashes are required. --leap maps
// to the LeapCommand setting, which is Type: LLSD, so the value is parsed as
// LLSD notation and a bare path fails outright with no fallback (see
// LLControlVariable::setValueFromNotation). What the notation parser returns is
// then split with backslash as an escape character, so a Windows path written
// the usual way loses its separators (see LLStringUtil::getTokens in
// llleap.cpp). Either mistake means this program is never launched at all.
//
// stdout belongs to the LEAP protocol and nothing else. Every diagnostic goes
// to stderr, which the viewer copies into its own log, so a user who cannot be
// walked through a terminal can just send their viewer log.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sync/atomic"
	"time"
)

// version is replaced at build time with the release tag:
//
//	go build -ldflags="-X main.version=v1.2.3"
var version = "dev"

// nullUUID is what LLAgent.getID returns before login completes.
const nullUUID = "00000000-0000-0000-0000-000000000000"

// chatPump is the viewer-side event stream carrying nearby chat
// (LLFloaterIMNearbyChatHandler::sChatWatcher).
const chatPump = "LLChat"

const listenerName = "sl-nearby-tts"

func main() {
	opts := DefaultOptions()
	var (
		dryRun      = flag.Bool("dry-run", false, "print lines to stderr instead of speaking them")
		voice       = flag.String("voice", "", "voice name; see -list-voices")
		rate        = flag.String("rate", "", "speaking rate (Windows -10..10, macOS words per minute, Linux backend-specific)")
		volume      = flag.String("volume", "", "volume 0..100 (Windows and Linux only)")
		queueDepth  = flag.Int("queue", 6, "how many lines may wait to be spoken before the oldest are dropped")
		logPath     = flag.String("log", "", "also append diagnostics to this file")
		listV       = flag.Bool("list-voices", false, "list installed voices and exit")
		selfTest    = flag.Bool("selftest", false, "speak a few sample lines and exit, without a viewer")
		showVer     = flag.Bool("version", false, "print version and exit")
		selfID      = flag.String("self", "", "your own avatar UUID; normally detected automatically")
		speakSelf   = flag.Bool("speak-self", false, "also speak your own messages")
		skipObjects = flag.Bool("skip-objects", false, "ignore chat from scripted objects")
		skipSystem  = flag.Bool("skip-system", false, "ignore viewer and region messages")
		speakDebug  = flag.Bool("speak-debug", false, "speak script errors from the debug channel")
		speakURLs   = flag.Bool("speak-urls", false, "read links aloud instead of saying \"link\"")
		maxChars    = flag.Int("max-chars", opts.MaxChars, "truncate messages longer than this")
		nameWindow  = flag.Duration("name-window", opts.NameWindow, "how long one speaker keeps the floor before being named again")
		dedupWindow = flag.Duration("dedup-window", opts.DedupWindow, "identical repeats within this window are dropped")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("sl-nearby-tts", version)
		return
	}

	logf := newLogger(*logPath)

	if *listV {
		out, err := listVoices()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(out)
		return
	}

	opts.SelfID = *selfID
	opts.SpeakSelf = *speakSelf
	opts.SkipObjects = *skipObjects
	opts.SkipSystem = *skipSystem
	opts.SpeakDebug = *speakDebug
	opts.SpeakURLs = *speakURLs
	opts.MaxChars = *maxChars
	opts.NameWindow = *nameWindow
	opts.DedupWindow = *dedupWindow

	speaker := newSpeaker(*dryRun, SpeechOpts{
		Voice:  *voice,
		Rate:   *rate,
		Volume: *volume,
		Logf:   logf,
	}, logf)
	queue := NewQueue(speaker, *queueDepth, logf)
	defer queue.Close()

	if *selfTest {
		selfTestRun(NewNarrator(opts), queue, logf)
		return
	}

	if err := run(os.Stdin, os.Stdout, NewNarrator(opts), queue, logf); err != nil && !errors.Is(err, io.EOF) {
		logf("exiting: %v", err)
		queue.Close()
		os.Exit(1)
	}
	logf("viewer closed the connection, %d line(s) dropped while behind", queue.Dropped())
}

// newLogger returns a logging function that writes to stderr, and to a file as
// well when one was asked for.
func newLogger(path string) func(string, ...any) {
	w := io.Writer(os.Stderr)
	if path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot open -log %s: %v\n", path, err)
		} else {
			w = io.MultiWriter(os.Stderr, f)
		}
	}
	l := log.New(w, listenerName+": ", log.LstdFlags)
	return func(format string, args ...any) { l.Printf(format, args...) }
}

// newSpeaker picks the platform backend, falling back to printing if speech is
// unavailable, so that a missing voice degrades to a readable log instead of a
// silent failure to start.
func newSpeaker(dryRun bool, so SpeechOpts, logf func(string, ...any)) Speaker {
	// Dry-run output goes to stderr: stdout is the LEAP channel and anything
	// extra on it desynchronizes the viewer.
	if dryRun {
		logf("dry run: lines will be printed, not spoken")
		return &dryRunSpeaker{w: os.Stderr}
	}
	sp, err := newPlatformSpeaker(so)
	if err != nil {
		logf("no speech available (%v); falling back to printing lines", err)
		return &dryRunSpeaker{w: os.Stderr}
	}
	return sp
}

// run drives one LEAP session: handshake, subscribe to nearby chat, then speak
// whatever arrives until the viewer closes the pipe.
func run(in io.Reader, out io.Writer, n *Narrator, q *Queue, logf func(string, ...any)) error {
	conn := NewConn(in, out)
	if err := conn.Handshake(); err != nil {
		return err
	}
	logf("version %s connected; reply pump %q, command pump %q", version, conn.ReplyPump, conn.CommandPump)

	if err := conn.Listen(chatPump, listenerName); err != nil {
		return err
	}
	// The viewer answers the subscription with a status, and a false one means
	// we are deaf for the rest of the session -- worth saying out loud in the
	// log rather than appearing to work.
	awaitingListen := true

	// Learn our own avatar ID so that our own chat is not read back to us. The
	// plugin starts before login, when the ID is still null, so keep asking
	// until the answer is real.
	var selfKnown atomic.Bool
	if n.Opts.SelfID != "" {
		selfKnown.Store(true)
	} else if err := conn.Request("LLAgent", "getID", nil); err != nil {
		return err
	}
	stopAsking := make(chan struct{})
	defer close(stopAsking)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopAsking:
				return
			case <-t.C:
				if selfKnown.Load() {
					return
				}
				if err := conn.Request("LLAgent", "getID", nil); err != nil {
					return
				}
			}
		}
	}()

	// Muting happens in here rather than by exiting: the viewer launches this
	// process once and never again, so a plugin that quits is gone until the
	// viewer restarts.
	muted := false

	for {
		f, err := conn.ReadFrame()
		if err != nil {
			return err
		}
		switch f.Pump {
		case chatPump:
			c := parseChat(f.Data)
			if cmd, ok := chatCommand(c, n.Opts.SelfID); ok {
				muted = cmd == cmdMute
				if muted {
					// A backlog spoken after "tts off" is exactly what was asked
					// to stop.
					q.Drop()
					q.Offer("chat muted")
				} else {
					q.Offer("chat unmuted")
				}
				logf("in-world command: %s", cmd)
				continue
			}
			if muted {
				continue
			}
			if line, ok := n.Line(c, time.Now()); ok {
				q.Offer(line)
			}
		case conn.ReplyPump:
			// Replies to our own requests: the subscription status, and our ID.
			if status, ok := f.Data["status"]; ok && awaitingListen {
				awaitingListen = false
				if ok, _ := status.(bool); ok {
					logf("subscribed to %s", chatPump)
				} else {
					logf("the viewer refused to subscribe us to %s; no chat will be spoken", chatPump)
				}
			}
			if id := lsdString(f.Data["id"]); id != "" && id != nullUUID && !selfKnown.Load() {
				n.Opts.SelfID = id
				selfKnown.Store(true)
				logf("own avatar id %s; own chat will be skipped", id)
			}
			if e := lsdString(f.Data["error"]); e != "" {
				logf("viewer reported: %s", e)
			}
		default:
			logf("ignoring event from unexpected pump %q", f.Pump)
		}
	}
}

// selfTestRun speaks a handful of representative lines so that someone can
// confirm speech works on their own machine without logging in.
func selfTestRun(n *Narrator, q *Queue, logf func(string, ...any)) {
	logf("self test: speaking sample lines")
	samples := []Chat{
		{From: "Test Resident", FromID: "11111111-1111-1111-1111-111111111111", Message: "Nearby chat is working.", Source: srcAgent, ChatType: chatNormal},
		{From: "Test Resident", FromID: "11111111-1111-1111-1111-111111111111", Message: "This line is from the same speaker, so the name is not repeated.", Source: srcAgent, ChatType: chatNormal},
		{From: "Someone Else", FromID: "22222222-2222-2222-2222-222222222222", Message: "/me waves hello", Source: srcAgent, ChatType: chatNormal, Style: styleIRC},
		{From: "Someone Else", FromID: "22222222-2222-2222-2222-222222222222", Message: "Check this out: https://secondlife.com/some/long/link", Source: srcAgent, ChatType: chatShout},
		{From: "A Vendor", FromID: "33333333-3333-3333-3333-333333333333", Message: "Thank you for your purchase.", Source: srcObject, ChatType: chatNormal},
	}
	now := time.Now()
	for i, c := range samples {
		line, ok := n.Line(c, now.Add(time.Duration(i)*time.Second))
		if !ok {
			logf("sample %d filtered out", i+1)
			continue
		}
		// In dry run the speaker prints the line itself; don't say it twice.
		if _, quiet := q.sp.(*dryRunSpeaker); !quiet {
			logf("speaking: %s", line)
		}
		if err := q.sp.Speak(line); err != nil {
			logf("speak failed: %v", err)
			return
		}
	}
	logf("self test done")
}

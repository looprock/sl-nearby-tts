package main

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSendFraming(t *testing.T) {
	var buf bytes.Buffer
	c := NewConn(nil, &buf)
	if err := c.Send("somepump", map[string]any{"op": "ping"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Byte count, colon, then exactly that many bytes of notation, no newline.
	got := buf.String()
	want := `{'data':{'op':'ping'},'pump':'somepump'}`
	if got != "40:"+want {
		t.Fatalf("Send wrote %q, want %q", got, "40:"+want)
	}
	if len(want) != 40 {
		t.Fatalf("test is wrong: payload is %d bytes", len(want))
	}
}

func TestReadFrame(t *testing.T) {
	payload := `{'pump':'LLChat','data':{'message':'hi'}}`
	in := strings.NewReader(strconv.Itoa(len(payload)) + ":" + payload)
	f, err := NewConn(in, nil).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Pump != "LLChat" {
		t.Errorf("pump = %q", f.Pump)
	}
	if f.Data["message"] != "hi" {
		t.Errorf("data = %#v", f.Data)
	}
}

func TestReadFrameErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no colon":          "123",
		"junk prefix":       "abc:{}",
		"empty prefix":      ":{}",
		"short body":        "40:{'pump':'x'}",
		"not a map":         "2:i1",
		"absurd length":     "99999999999999:{}",
		"oversized length":  "17000000:{}",
		"bad notation body": "3:{'x",
	} {
		if _, err := NewConn(strings.NewReader(in), nil).ReadFrame(); err == nil {
			t.Errorf("%s: want error for %q", name, in)
		}
	}
}

func TestReadFrameCleanEOF(t *testing.T) {
	_, err := NewConn(strings.NewReader(""), nil).ReadFrame()
	if err != io.EOF {
		t.Errorf("got %v, want io.EOF on a closed pipe", err)
	}
}

func TestHandshakeRequiresPumpNames(t *testing.T) {
	// A first message without a command pump is not a usable session.
	payload := `{'pump':'reply','data':{}}`
	err := NewConn(strings.NewReader(strconv.Itoa(len(payload))+":"+payload), nil).Handshake()
	if err == nil {
		t.Error("want error when the handshake has no command pump")
	}
}

type captureSpeaker struct{ lines chan string }

func (c *captureSpeaker) Speak(s string) error { c.lines <- s; return nil }
func (c *captureSpeaker) Close() error         { return nil }

// next returns the next spoken line, or fails if nothing is spoken in time.
func (c *captureSpeaker) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-c.lines:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a spoken line")
		return ""
	}
}

func (c *captureSpeaker) silent(t *testing.T) {
	t.Helper()
	select {
	case s := <-c.lines:
		t.Fatalf("spoke %q, want silence", s)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestMockViewerSession plays the viewer's half of the protocol end to end: it
// performs the handshake, checks that the plugin subscribes to the nearby-chat
// pump and asks for the agent's own ID, then feeds chat events and asserts what
// comes out of the speaker. This is what stands in for a real viewer on a
// platform we cannot run one on.
func TestMockViewerSession(t *testing.T) {
	pluginSide, viewerSide := net.Pipe()
	defer viewerSide.Close()

	const selfID = "0f0f0f0f-0000-0000-0000-00000000beef"

	cap := &captureSpeaker{lines: make(chan string, 32)}
	queue := NewQueue(cap, 32, func(string, ...any) {})
	narrator := NewNarrator(DefaultOptions())

	done := make(chan error, 1)
	go func() {
		done <- run(pluginSide, pluginSide, narrator, queue, func(string, ...any) {})
	}()

	viewer := NewConn(viewerSide, viewerSide)

	// 1. The preliminary message, exactly as LLLeapImpl sends it.
	if err := viewer.Send("reply-pump-7", map[string]any{
		"command":  "cmd-pump-7",
		"features": map[string]any{},
	}); err != nil {
		t.Fatalf("sending handshake: %v", err)
	}

	// 2. The plugin should subscribe to LLChat on the command pump.
	f, err := viewer.ReadFrame()
	if err != nil {
		t.Fatalf("reading listen request: %v", err)
	}
	if f.Pump != "cmd-pump-7" {
		t.Errorf("listen sent to pump %q, want the command pump", f.Pump)
	}
	if f.Data["op"] != "listen" || f.Data["source"] != chatPump {
		t.Errorf("listen request = %#v", f.Data)
	}
	if f.Data["reply"] != "reply-pump-7" {
		t.Errorf("listen reply pump = %#v, want ours", f.Data["reply"])
	}

	// 3. And ask the LLAgent API who we are.
	f, err = viewer.ReadFrame()
	if err != nil {
		t.Fatalf("reading getID request: %v", err)
	}
	if f.Pump != "LLAgent" || f.Data["op"] != "getID" {
		t.Errorf("second request = pump %q data %#v, want LLAgent getID", f.Pump, f.Data)
	}
	if err := viewer.Send("reply-pump-7", map[string]any{"id": UUID(selfID)}); err != nil {
		t.Fatalf("replying with id: %v", err)
	}

	// 4. Real chat, in the shape the viewer posts to the LLChat stream.
	chat := func(m map[string]any) {
		t.Helper()
		if err := viewer.Send(chatPump, m); err != nil {
			t.Fatalf("sending chat: %v", err)
		}
	}

	chat(map[string]any{
		"message": "hello there", "from": "Alice Resident",
		"from_id": UUID("aaaa1111-0000-0000-0000-000000000001"),
		"source":  int64(srcAgent), "chat_type": int64(chatNormal),
		"chat_style": int64(styleNormal), "time": 1234.5,
	})
	if got := cap.next(t); got != "Alice says, hello there" {
		t.Errorf("spoke %q", got)
	}

	// Our own chat must not be read back at us.
	chat(map[string]any{
		"message": "this is me talking", "from": "Me Resident",
		"from_id": UUID(selfID),
		"source":  int64(srcAgent), "chat_type": int64(chatNormal),
	})
	cap.silent(t)

	// Typing indicators are not speech.
	chat(map[string]any{
		"message": "typing", "from": "Alice Resident",
		"from_id": UUID("aaaa1111-0000-0000-0000-000000000001"),
		"source":  int64(srcAgent), "chat_type": int64(chatStart),
	})
	cap.silent(t)

	// Neither is the backlog the viewer replays into the window.
	chat(map[string]any{
		"message": "old line", "from": "Alice Resident",
		"from_id": UUID("aaaa1111-0000-0000-0000-000000000001"),
		"source":  int64(srcAgent), "chat_type": int64(chatNormal),
		"chat_style": int64(styleHistory),
	})
	cap.silent(t)

	// A shout from someone else, with a link that should not be read out.
	chat(map[string]any{
		"message": "/me points at https://example.com/x", "from": "Bob Resident",
		"from_id": UUID("bbbb2222-0000-0000-0000-000000000002"),
		"source":  int64(srcAgent), "chat_type": int64(chatShout),
	})
	if got := cap.next(t); got != "Bob points at link" {
		t.Errorf("spoke %q", got)
	}

	// Non-ASCII survives the hex escaping the viewer applies to every such byte.
	chat(map[string]any{
		"message": "café ✓", "from": "Chloé Resident",
		"from_id": UUID("cccc3333-0000-0000-0000-000000000003"),
		"source":  int64(srcAgent), "chat_type": int64(chatNormal),
	})
	if got := cap.next(t); got != "Chloé says, café ✓" {
		t.Errorf("spoke %q", got)
	}

	// Events from a pump we never subscribed to are ignored, not fatal.
	if err := viewer.Send("SomeOtherPump", map[string]any{"x": int64(1)}); err != nil {
		t.Fatalf("sending stray event: %v", err)
	}
	cap.silent(t)

	// A malformed chat event should not take the session down.
	chat(map[string]any{"message": int64(7), "from": nil})
	cap.silent(t)
	chat(map[string]any{
		"message": "still alive", "from": "Alice Resident",
		"from_id": UUID("aaaa1111-0000-0000-0000-000000000001"),
		"source":  int64(srcAgent), "chat_type": int64(chatNormal),
	})
	if got := cap.next(t); got != "Alice says, still alive" {
		t.Errorf("spoke %q", got)
	}

	// Closing the pipe is how the viewer signals shutdown.
	viewerSide.Close()
	select {
	case err := <-done:
		if err != nil && err != io.EOF {
			t.Errorf("run returned %v, want a clean EOF", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after the viewer closed the pipe")
	}
	if err := queue.Close(); err != nil {
		t.Errorf("queue.Close: %v", err)
	}
}

func TestQueueDropsOldestWhenBehind(t *testing.T) {
	// A speaker that blocks until released, so the queue is guaranteed to fill.
	blocking := &blockingSpeaker{
		release: make(chan struct{}),
		entered: make(chan struct{}, 1),
		spoken:  make(chan string, 16),
	}
	spoken := blocking.spoken

	q := NewQueue(blocking, 2, func(string, ...any) {})
	q.Offer("first")
	// Wait until the speaking goroutine is actually inside Speak and holding,
	// otherwise "first" is still queued and would itself be dropped.
	select {
	case <-blocking.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("speaker never started")
	}
	q.Offer("second") // queued
	q.Offer("third")  // queued
	q.Offer("fourth") // queue full: "second" is dropped
	q.Offer("fifth")  // "third" is dropped

	close(blocking.release)
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(spoken)

	var got []string
	for s := range spoken {
		got = append(got, s)
	}
	if len(got) == 0 || got[0] != "first" {
		t.Fatalf("spoke %v, want it to start with first", got)
	}
	last := got[len(got)-1]
	if last != "fifth" {
		t.Errorf("spoke %v, want the newest line kept, got last = %q", got, last)
	}
	if q.Dropped() == 0 {
		t.Error("want the dropped count to be reported")
	}
}

type blockingSpeaker struct {
	release chan struct{}
	entered chan struct{}
	spoken  chan string
	started bool
}

func (b *blockingSpeaker) Speak(s string) error {
	b.spoken <- s
	if !b.started {
		b.started = true
		b.entered <- struct{}{}
		<-b.release // hold the queue open so Offer has to drop
	}
	return nil
}

func (b *blockingSpeaker) Close() error { return nil }

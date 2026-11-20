package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBinaryAgainstMockViewer builds the real program and drives it as a child
// process, playing the viewer's side of the protocol over its stdin and stdout.
//
// The in-process tests cover the logic; this one covers the thing that only
// shows up in a real process: anything at all written to stdout that is not a
// length-prefixed frame desynchronizes the viewer permanently. A stray print
// or a library writing to stdout would pass every other test and fail here.
func TestBinaryAgainstMockViewer(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	bin := filepath.Join(t.TempDir(), "sl-nearby-tts")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building: %v", err)
	}

	// -dry-run keeps the test silent and sends the would-be speech to stderr,
	// which is also where every diagnostic belongs.
	cmd := exec.Command(bin, "-dry-run", "-name-window", "1ms")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", bin, err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	// Collect the lines it says it would speak.
	spoken := make(chan string, 16)
	var logged strings.Builder
	var mu sync.Mutex
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			logged.WriteString(line + "\n")
			mu.Unlock()
			if i := strings.Index(line, "SPEAK: "); i >= 0 {
				spoken <- line[i+len("SPEAK: "):]
			}
		}
	}()
	next := func() string {
		t.Helper()
		select {
		case s := <-spoken:
			return s
		case <-time.After(10 * time.Second):
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("timed out waiting for speech; stderr so far:\n%s", logged.String())
			return ""
		}
	}

	viewer := NewConn(stdout, stdin)
	if err := viewer.Send("reply-pump", map[string]any{
		"command": "cmd-pump", "features": map[string]any{},
	}); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// Every frame it writes must parse. ReadFrame fails on any extra byte.
	f, err := viewer.ReadFrame()
	if err != nil {
		t.Fatalf("reading the listen request (stdout may be polluted): %v", err)
	}
	if f.Pump != "cmd-pump" || f.Data["op"] != "listen" || f.Data["source"] != chatPump {
		t.Fatalf("first frame = pump %q data %#v", f.Pump, f.Data)
	}
	if f, err = viewer.ReadFrame(); err != nil {
		t.Fatalf("reading the getID request: %v", err)
	}
	if f.Pump != "LLAgent" || f.Data["op"] != "getID" {
		t.Fatalf("second frame = pump %q data %#v", f.Pump, f.Data)
	}

	if err := viewer.Send(chatPump, map[string]any{
		"message": "it works", "from": "Alice Resident",
		"from_id": UUID("aaaa1111-0000-0000-0000-000000000001"),
		"source":  int64(srcAgent), "chat_type": int64(chatNormal),
	}); err != nil {
		t.Fatalf("sending chat: %v", err)
	}
	if got := next(); got != "Alice says, it works" {
		t.Errorf("spoke %q", got)
	}

	// Text that the viewer hex-escapes byte by byte must come back whole.
	if err := viewer.Send(chatPump, map[string]any{
		"message": "ünïcödé ✓", "from": "Bob Resident",
		"from_id": UUID("bbbb2222-0000-0000-0000-000000000002"),
		"source":  int64(srcAgent), "chat_type": int64(chatNormal),
	}); err != nil {
		t.Fatalf("sending chat: %v", err)
	}
	if got := next(); got != "Bob says, ünïcödé ✓" {
		t.Errorf("spoke %q", got)
	}

	// Closing stdin is how the viewer ends a session; the plugin should exit 0.
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			mu.Lock()
			defer mu.Unlock()
			t.Errorf("exited with %v; stderr:\n%s", err, logged.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Error("did not exit after stdin closed")
	}
}

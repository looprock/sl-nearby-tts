package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var base = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func agentChat(from, msg string) Chat {
	return Chat{From: from, FromID: "aaaa1111-0000-0000-0000-000000000001", Message: msg,
		Source: srcAgent, ChatType: chatNormal, Style: styleNormal}
}

func TestLineDropsNonSpeech(t *testing.T) {
	for name, c := range map[string]Chat{
		"started typing": {From: "A", Message: "x", ChatType: chatStart, Source: srcAgent},
		"stopped typing": {From: "A", Message: "x", ChatType: chatStop, Source: srcAgent},
		"script debug":   {From: "A", Message: "x", ChatType: chatDebug, Source: srcObject},
		"replayed log":   {From: "A", Message: "x", ChatType: chatNormal, Style: styleHistory, Source: srcAgent},
		"teleport sep":   {From: "A", Message: "x", ChatType: chatNormal, Style: styleTeleport, Source: srcAgent},
		"empty":          {From: "A", Message: "   ", ChatType: chatNormal, Source: srcAgent},
	} {
		n := NewNarrator(DefaultOptions())
		if line, ok := n.Line(c, base); ok {
			t.Errorf("%s: spoke %q, want it dropped", name, line)
		}
	}
}

// Replayed history is the one that bites in practice: without this the plugin
// recites the entire chat window every time the viewer repopulates it.
func TestLineSpeaksDebugOnlyWhenAsked(t *testing.T) {
	c := Chat{From: "Script", Message: "syntax error", ChatType: chatDebug, Source: srcObject}
	o := DefaultOptions()
	o.SpeakDebug = true
	n := NewNarrator(o)
	if _, ok := n.Line(c, base); !ok {
		t.Error("want debug chat spoken when -speak-debug is set")
	}
}

func TestLineSkipsOwnChat(t *testing.T) {
	o := DefaultOptions()
	o.SelfID = "mymy1111-0000-0000-0000-000000000009"
	n := NewNarrator(o)
	mine := Chat{From: "Me Resident", FromID: o.SelfID, Message: "talking to myself",
		Source: srcAgent, ChatType: chatNormal}
	if line, ok := n.Line(mine, base); ok {
		t.Errorf("spoke own chat: %q", line)
	}

	o.SpeakSelf = true
	n = NewNarrator(o)
	if _, ok := n.Line(mine, base); !ok {
		t.Error("want own chat spoken when -speak-self is set")
	}
}

func TestLineObjectAndSystemFilters(t *testing.T) {
	obj := Chat{From: "Vendor", Message: "buy this", Source: srcObject, ChatType: chatNormal}
	sys := Chat{From: "", Message: "Teleport completed", Source: srcSystem, ChatType: chatNormal}

	o := DefaultOptions()
	n := NewNarrator(o)
	if _, ok := n.Line(obj, base); !ok {
		t.Error("objects should be spoken by default")
	}
	if _, ok := n.Line(sys, base); !ok {
		t.Error("system messages should be spoken by default")
	}

	o.SkipObjects, o.SkipSystem = true, true
	n = NewNarrator(o)
	if _, ok := n.Line(obj, base); ok {
		t.Error("want object chat skipped")
	}
	if _, ok := n.Line(sys, base); ok {
		t.Error("want system chat skipped")
	}
}

func TestLinePhrasing(t *testing.T) {
	for name, tc := range map[string]struct {
		chat Chat
		want string
	}{
		"normal": {agentChat("Alice Resident", "hello"), "Alice says, hello"},
		"whisper": {Chat{From: "Alice Resident", Message: "psst", Source: srcAgent, ChatType: chatWhisper},
			"Alice whispers, psst"},
		"shout": {Chat{From: "Alice Resident", Message: "hey!", Source: srcAgent, ChatType: chatShout},
			"Alice shouts, hey!"},
		"emote by style": {Chat{From: "Alice Resident", Message: "/me waves", Source: srcAgent,
			ChatType: chatNormal, Style: styleIRC}, "Alice waves"},
		"emote by prefix": {agentChat("Alice Resident", "/me grins"), "Alice grins"},
		"possessive emote": {agentChat("Alice Resident", "/me's hat falls off"),
			"Alice's hat falls off"},
		"object": {Chat{From: "Gift Box", Message: "thank you", Source: srcObject, ChatType: chatNormal},
			"Gift Box, thank you"},
		"system": {Chat{From: "", Message: "Teleport completed", Source: srcSystem, ChatType: chatNormal},
			"Teleport completed"},
		"display name trimmed": {agentChat("alice.resident (Alice!)", "hi"),
			"alice.resident says, hi"},
		"url replaced": {agentChat("Alice Resident", "see https://example.com/a/b now"),
			"Alice says, see link now"},
		"slurl replaced": {agentChat("Alice Resident", "at secondlife:///app/agent/x/about"),
			"Alice says, at link"},
		"whitespace collapsed": {agentChat("Alice Resident", "too    many\t\tspaces"),
			"Alice says, too many spaces"},
	} {
		n := NewNarrator(DefaultOptions())
		got, ok := n.Line(tc.chat, base)
		if !ok {
			t.Errorf("%s: dropped, want %q", name, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestLineKeepsURLsWhenAsked(t *testing.T) {
	o := DefaultOptions()
	o.SpeakURLs = true
	n := NewNarrator(o)
	got, _ := n.Line(agentChat("Alice Resident", "see https://example.com now"), base)
	if !strings.Contains(got, "https://example.com") {
		t.Errorf("got %q, want the URL kept", got)
	}
}

// One speaker holding the floor should not have their name repeated on every
// line, but a gap or another voice should bring it back.
func TestLineNameRepetition(t *testing.T) {
	n := NewNarrator(DefaultOptions())
	first, _ := n.Line(agentChat("Alice Resident", "one"), base)
	if first != "Alice says, one" {
		t.Fatalf("first = %q", first)
	}
	second, _ := n.Line(agentChat("Alice Resident", "two"), base.Add(2*time.Second))
	if second != "two" {
		t.Errorf("second = %q, want the bare message", second)
	}
	later, _ := n.Line(agentChat("Alice Resident", "three"), base.Add(5*time.Minute))
	if later != "Alice says, three" {
		t.Errorf("after a gap = %q, want the name again", later)
	}

	n = NewNarrator(DefaultOptions())
	n.Line(agentChat("Alice Resident", "one"), base)
	other := Chat{From: "Bob Resident", FromID: "bbbb", Message: "two", Source: srcAgent, ChatType: chatNormal}
	got, _ := n.Line(other, base.Add(time.Second))
	if got != "Bob says, two" {
		t.Errorf("other speaker = %q, want them named", got)
	}
}

func TestLineDedupesRepeats(t *testing.T) {
	n := NewNarrator(DefaultOptions())
	if _, ok := n.Line(agentChat("Alice Resident", "same"), base); !ok {
		t.Fatal("first copy dropped")
	}
	if line, ok := n.Line(agentChat("Alice Resident", "same"), base.Add(time.Second)); ok {
		t.Errorf("spoke immediate repeat: %q", line)
	}
	// Outside the dedup window it is a real repetition, not an echo.
	if _, ok := n.Line(agentChat("Alice Resident", "same"), base.Add(10*time.Second)); !ok {
		t.Error("want the same line spoken again later")
	}
}

func TestTruncate(t *testing.T) {
	o := DefaultOptions()
	o.MaxChars = 40
	n := NewNarrator(o)
	long := strings.Repeat("word ", 50)
	got, ok := n.Line(agentChat("Alice Resident", long), base)
	if !ok {
		t.Fatal("dropped")
	}
	if len(got) > 80 {
		t.Errorf("length %d, want it truncated: %q", len(got), got)
	}
	if !strings.HasSuffix(got, "and so on") {
		t.Errorf("got %q, want a truncation marker", got)
	}
}

func TestSpeakableName(t *testing.T) {
	for in, want := range map[string]string{
		"Alice Resident":          "Alice",
		"Alice Liddell":           "Alice Liddell",
		"alice.resident (Alice!)": "alice.resident",
		"  Spaced  ":              "Spaced",
		"":                        "",
	} {
		if got := speakableName(in); got != want {
			t.Errorf("speakableName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Truncating at a byte offset must not split a multi-byte character, or the
// synthesizer is handed invalid UTF-8.
func TestTruncateKeepsValidUTF8(t *testing.T) {
	o := DefaultOptions()
	o.MaxChars = 11
	n := NewNarrator(o)
	// Each of these is two bytes, so a byte-offset cut lands mid-character.
	got, ok := n.Line(agentChat("A", strings.Repeat("é", 40)), base)
	if !ok {
		t.Fatal("dropped")
	}
	if !utf8.ValidString(got) {
		t.Errorf("produced invalid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("produced a replacement character: %q", got)
	}
}

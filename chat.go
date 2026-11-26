package main

// Turning one nearby-chat event into one spoken line: which messages are worth
// speaking at all, and how to word them so they are bearable to listen to.

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Values of the "source" field (EChatSourceType in indra/llui/llchat.h).
const (
	srcSystem   = 0
	srcAgent    = 1
	srcObject   = 2
	srcTeleport = 3
	srcUnknown  = 4
	srcRegion   = 5
)

// Values of the "chat_type" field (EChatType).
const (
	chatWhisper = 0
	chatNormal  = 1
	chatShout   = 2
	chatStart   = 4 // started typing
	chatStop    = 5 // stopped typing
	chatDebug   = 6 // script error / debug channel
	chatRegion  = 7
	chatOwner   = 8 // llOwnerSay
	chatDirect  = 9 // llRegionSayTo
)

// Values of the "chat_style" field (EChatStyle).
const (
	styleNormal   = 0
	styleIRC      = 1 // an emote, i.e. "/me waves"
	styleHistory  = 2 // backlog replayed into the window, never new speech
	styleTeleport = 3
)

// Chat is one decoded message off the LLChat pump.
type Chat struct {
	Message  string
	From     string
	FromID   string
	Time     float64 // seconds since viewer start, not wall clock
	Source   int
	ChatType int
	Style    int
}

func parseChat(data map[string]any) Chat {
	return Chat{
		Message:  lsdString(data["message"]),
		From:     lsdString(data["from"]),
		FromID:   lsdString(data["from_id"]),
		Time:     lsdFloat(data["time"]),
		Source:   lsdInt(data["source"]),
		ChatType: lsdInt(data["chat_type"]),
		Style:    lsdInt(data["chat_style"]),
	}
}

func lsdString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case UUID:
		return string(t)
	case URI:
		return string(t)
	}
	return ""
}

func lsdInt(v any) int {
	switch t := v.(type) {
	case int64:
		return int(t)
	case int:
		return t
	case float64:
		return int(t)
	case bool:
		if t {
			return 1
		}
	}
	return 0
}

func lsdFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	}
	return 0
}

// Options controls what gets spoken and how.
type Options struct {
	SelfID      string        // our own agent UUID; its chat is skipped
	SpeakSelf   bool          // speak our own messages too
	SkipObjects bool          // drop chat from scripted objects
	SkipSystem  bool          // drop viewer/system messages
	SpeakDebug  bool          // speak script errors on the debug channel
	MaxChars    int           // truncate anything longer
	NameWindow  time.Duration // how long one speaker holds the floor before we name them again
	DedupWindow time.Duration // identical message from the same source inside this window is a repeat
	SpeakURLs   bool          // read links out instead of saying "link"
}

func DefaultOptions() Options {
	return Options{
		MaxChars:    350,
		NameWindow:  30 * time.Second,
		DedupWindow: 3 * time.Second,
	}
}

// Narrator converts chat events to utterances, remembering just enough history
// to avoid repeating a speaker's name on every line and to swallow duplicates.
type Narrator struct {
	Opts Options

	lastFrom string
	lastText string
	lastAt   time.Time
}

func NewNarrator(o Options) *Narrator {
	if o.MaxChars <= 0 {
		o.MaxChars = DefaultOptions().MaxChars
	}
	return &Narrator{Opts: o}
}

var (
	urlRe   = regexp.MustCompile(`(?i)\b(?:https?://|secondlife://|www\.)\S+`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// Line returns the text to speak, or ok=false if this event should be ignored.
func (n *Narrator) Line(c Chat, now time.Time) (string, bool) {
	if !n.Opts.SpeakDebug && c.ChatType == chatDebug {
		return "", false
	}
	switch c.ChatType {
	case chatStart, chatStop:
		// Typing indicators are events, not speech.
		return "", false
	}
	switch c.Style {
	case styleHistory, styleTeleport:
		// Replayed backlog. Speaking this means reciting the whole window on login.
		return "", false
	}
	if c.Source == srcObject && n.Opts.SkipObjects {
		return "", false
	}
	if (c.Source == srcSystem || c.Source == srcRegion) && n.Opts.SkipSystem {
		return "", false
	}
	if !n.Opts.SpeakSelf && n.Opts.SelfID != "" && c.FromID == n.Opts.SelfID {
		return "", false
	}

	text := strings.TrimSpace(c.Message)
	if text == "" {
		return "", false
	}

	// An emote is either flagged as such or written in the "/me" form.
	emote := c.Style == styleIRC
	if rest, ok := cutEmote(text); ok {
		emote = true
		text = rest
	}

	if !n.Opts.SpeakURLs {
		text = urlRe.ReplaceAllString(text, "link")
	}
	text = spaceRe.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	text = truncate(text, n.Opts.MaxChars)

	// Gestures and relays often emit the same line twice in quick succession.
	if c.From == n.lastFrom && text == n.lastText && !n.lastAt.IsZero() &&
		now.Sub(n.lastAt) < n.Opts.DedupWindow {
		return "", false
	}

	named := n.lastFrom != c.From || n.lastAt.IsZero() || now.Sub(n.lastAt) > n.Opts.NameWindow
	n.lastFrom = c.From
	n.lastText = text
	n.lastAt = now

	name := speakableName(c.From)
	switch {
	case emote && name != "":
		// "/me's hat falls off" reads as "Alice's hat falls off", with no space
		// before the possessive.
		if strings.HasPrefix(text, "'") {
			return name + text, true
		}
		return name + " " + text, true
	case c.Source == srcSystem || c.Source == srcRegion:
		// A system line reads as a sentence about its subject, with the name in
		// "from" and the predicate in "message": "Emilylo Mayako" + "is
		// online." Joining them with "says" would be wrong, and dropping the
		// name leaves a bare "is online".
		if name != "" {
			return name + " " + text, true
		}
		return text, true
	case name == "":
		// Nothing to attribute this to.
		return text, true
	case !named:
		// Same voice still holding the floor; naming them again is noise.
		return text, true
	case c.ChatType == chatWhisper:
		return name + " whispers, " + text, true
	case c.ChatType == chatShout:
		return name + " shouts, " + text, true
	case c.Source == srcObject:
		return name + ", " + text, true
	default:
		return name + " says, " + text, true
	}
}

// cutEmote strips a leading "/me" or "/me's", returning the remainder.
func cutEmote(s string) (string, bool) {
	if !strings.HasPrefix(s, "/me") {
		return s, false
	}
	rest := s[len("/me"):]
	switch {
	case strings.HasPrefix(rest, "'s "):
		return "'s " + strings.TrimSpace(rest[3:]), true
	case rest == "":
		return "", true
	case rest[0] == ' ':
		return strings.TrimSpace(rest), true
	}
	return s, false
}

// speakableName trims the parts of a Second Life name that are noise out loud:
// the "Resident" surname every modern account carries, and any display name in
// parentheses after the username.
func speakableName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " Resident")
	if i := strings.Index(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// truncate cuts over-long text at a word boundary so that one pasted wall of
// text cannot monopolize the speech queue.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, ' '); i > max/2 {
		cut = cut[:i]
	}
	// Cutting at a byte offset can land inside a multi-byte character; drop any
	// partial one rather than handing the synthesizer invalid UTF-8.
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRuneInString(cut); r == utf8.RuneError && size <= 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return strings.TrimSpace(cut) + ", and so on"
}

// Commands the user can give from inside the viewer. The plugin's process is
// started once by the viewer and lives as long as it does -- LLAppViewer::init
// calls LLLeap::create and nothing re-reads LeapCommand afterwards -- so going
// quiet has to mean falling silent, not exiting.
const (
	cmdMute   = "mute"
	cmdUnmute = "unmute"
)

// chatCommand recognizes a line as an instruction to this program rather than
// speech to read out.
//
// Only the user's own typing counts, or llOwnerSay from one of their own
// objects: the viewer shows llOwnerSay to the object's owner and to nobody
// else, which is what makes a HUD button a private alternative to typing the
// command in open chat. Either way the line reaches us because
// LLFloaterIMNearbyChatHandler posts everything the nearby chat window shows to
// the LLChat pump, our own chat included.
func chatCommand(c Chat, selfID string) (string, bool) {
	fromUser := selfID != "" && c.FromID == selfID
	if !fromUser && c.ChatType != chatOwner {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(c.Message)) {
	case "tts off":
		return cmdMute, true
	case "tts on":
		return cmdUnmute, true
	}
	return "", false
}

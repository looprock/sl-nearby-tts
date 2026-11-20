package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

// The Windows backend cannot be run from a non-Windows host, so the parts of it
// that are pure string handling are kept testable and tested here. Getting the
// -EncodedCommand encoding wrong fails silently: PowerShell starts, reads
// garbage, and never speaks.
func TestEncodedCommandIsUTF16LEBase64(t *testing.T) {
	const script = "Write-Output 'héllo'"
	enc := encodedCommand(script)

	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("not valid base64: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16 payload has an odd length (%d)", len(raw))
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8) // little endian
	}
	if got := string(utf16.Decode(units)); got != script {
		t.Errorf("decoded %q, want %q", got, script)
	}
}

func TestEncodedCommandHandlesTheRealScripts(t *testing.T) {
	for name, script := range map[string]string{
		"speech": psSpeechScript,
		"voices": psListVoicesScript,
	} {
		raw, err := base64.StdEncoding.DecodeString(encodedCommand(script))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		units := make([]uint16, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
		}
		if got := string(utf16.Decode(units)); got != script {
			t.Errorf("%s: script did not survive the encoding", name)
		}
	}
}

// Chat text must never be interpolated into PowerShell source. It arrives
// base64-encoded on stdin and is decoded inside the script, which is what stops
// an avatar in earshot from running code on the listener's machine by typing it.
func TestSpeechScriptDoesNotInterpolateInput(t *testing.T) {
	if !strings.Contains(psSpeechScript, "FromBase64String") {
		t.Error("the script should decode base64 input rather than parse it")
	}
	if !strings.Contains(psSpeechScript, "[Console]::In.ReadLine()") {
		t.Error("the script should read utterances from stdin")
	}
	// Invoke-Expression or an &/. call on input would reintroduce the risk.
	for _, bad := range []string{"Invoke-Expression", "iex ", "& $", ". $"} {
		if strings.Contains(psSpeechScript, bad) {
			t.Errorf("the script contains %q, which can execute input", bad)
		}
	}
}

// The script has to acknowledge each line, because that ack is what makes
// Speak block and the queue pace itself on real speech.
func TestSpeechScriptAcknowledgesEachLine(t *testing.T) {
	if !strings.Contains(psSpeechScript, `[Console]::Out.WriteLine("ok")`) {
		t.Error("the script must acknowledge each utterance on stdout")
	}
	if !strings.Contains(psSpeechScript, `[Console]::Out.WriteLine("ready `) {
		t.Error("the script must report readiness so startup failures surface immediately")
	}
	if strings.Count(psSpeechScript, "[Console]::Out.Flush()") < 2 {
		t.Error("the script must flush stdout, or acks sit in a buffer and Speak hangs")
	}
}

// Every diagnostic in the script must go to stderr. The program's stdout is the
// LEAP channel; an unexpected line on it desynchronizes the viewer for good.
func TestSpeechScriptKeepsDiagnosticsOffStdout(t *testing.T) {
	for _, line := range strings.Split(psSpeechScript, "\n") {
		if !strings.Contains(line, "WriteLine") {
			continue
		}
		if strings.Contains(line, "[Console]::Error.WriteLine") {
			continue
		}
		// The only things allowed on stdout are the handshake and the acks.
		if strings.Contains(line, `"ok"`) || strings.Contains(line, `"ready `) {
			continue
		}
		t.Errorf("writes to stdout outside the protocol: %s", strings.TrimSpace(line))
	}
}

//go:build windows

package main

import (
	"os"
	"testing"
)

// TestRealWindowsSpeech exercises the actual PowerShell speech backend, which
// needs a machine with a voice and an audio device. CI runners have neither, so
// this is opt-in: run it on the target machine with
//
//	set SLTTS_AUDIO_TEST=1 && go test -run TestRealWindowsSpeech -v
func TestRealWindowsSpeech(t *testing.T) {
	if os.Getenv("SLTTS_AUDIO_TEST") == "" {
		t.Skip("set SLTTS_AUDIO_TEST=1 to speak out loud")
	}
	sp, err := newPlatformSpeaker(SpeechOpts{
		Logf: func(f string, a ...any) { t.Logf(f, a...) },
	})
	if err != nil {
		t.Fatalf("starting the speech backend: %v", err)
	}
	defer sp.Close()

	for _, line := range []string{
		"Alice says, nearby chat is working.",
		"Bob shouts, and so is the queue.",
	} {
		if err := sp.Speak(line); err != nil {
			t.Fatalf("Speak(%q): %v", line, err)
		}
	}
}

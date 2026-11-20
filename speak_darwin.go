//go:build darwin

package main

// macOS speech, via the built-in say(1). Text goes in on stdin so that no chat
// message can ever be mistaken for an argument, and say blocks until it has
// finished speaking, which paces the queue for us.

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type saySpeaker struct {
	args []string
	opts SpeechOpts
}

func newPlatformSpeaker(o SpeechOpts) (Speaker, error) {
	if _, err := exec.LookPath("say"); err != nil {
		return nil, fmt.Errorf("say(1) not found: %w", err)
	}
	var args []string
	if o.Voice != "" {
		args = append(args, "-v", o.Voice)
	}
	if o.Rate != "" {
		if _, err := strconv.Atoi(o.Rate); err != nil {
			return nil, fmt.Errorf("-rate is words per minute on macOS (try 180), got %q", o.Rate)
		}
		args = append(args, "-r", o.Rate)
	}
	if o.Volume != "" {
		o.logf("-volume has no effect on macOS; use the system volume")
	}
	return &saySpeaker{args: args, opts: o}, nil
}

func (s *saySpeaker) Speak(text string) error {
	cmd := exec.Command("say", s.args...)
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("say: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *saySpeaker) Close() error { return nil }

func listVoices() (string, error) {
	out, err := exec.Command("say", "-v", "?").Output()
	if err != nil {
		return "", fmt.Errorf("listing voices: %w", err)
	}
	return string(out), nil
}

//go:build !windows && !darwin

package main

// Speech on everything else, which in practice means Linux: speech-dispatcher
// if it is installed, otherwise eSpeak NG. Both are invoked so that they block
// until the line has been spoken.

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type unixSpeaker struct {
	bin  string
	args []string
}

func newPlatformSpeaker(o SpeechOpts) (Speaker, error) {
	rate := ""
	if o.Rate != "" {
		if _, err := strconv.Atoi(o.Rate); err != nil {
			return nil, fmt.Errorf("-rate must be a whole number, got %q", o.Rate)
		}
		rate = o.Rate
	}

	if bin, err := exec.LookPath("spd-say"); err == nil {
		args := []string{"--wait"}
		if o.Voice != "" {
			args = append(args, "-y", o.Voice)
		}
		if rate != "" {
			args = append(args, "-r", rate)
		}
		if o.Volume != "" {
			args = append(args, "-i", o.Volume)
		}
		return &unixSpeaker{bin: bin, args: args}, nil
	}
	if bin, err := exec.LookPath("espeak-ng"); err == nil {
		var args []string
		if o.Voice != "" {
			args = append(args, "-v", o.Voice)
		}
		if rate != "" {
			args = append(args, "-s", rate)
		}
		return &unixSpeaker{bin: bin, args: args}, nil
	}
	return nil, fmt.Errorf("no speech backend found; install speech-dispatcher (spd-say) or espeak-ng")
}

func (s *unixSpeaker) Speak(text string) error {
	// Both backends take the text as a trailing argument. Go execs directly
	// without a shell, so the text needs no quoting, but "--" keeps a message
	// that starts with a dash from being read as a flag.
	args := append(append([]string{}, s.args...), "--", text)
	cmd := exec.Command(s.bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", s.bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *unixSpeaker) Close() error { return nil }

func listVoices() (string, error) {
	if bin, err := exec.LookPath("spd-say"); err == nil {
		out, err := exec.Command(bin, "-L").Output()
		if err == nil {
			return string(out), nil
		}
	}
	if bin, err := exec.LookPath("espeak-ng"); err == nil {
		out, err := exec.Command(bin, "--voices").Output()
		if err == nil {
			return string(out), nil
		}
	}
	return "", fmt.Errorf("no speech backend found")
}

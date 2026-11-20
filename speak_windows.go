//go:build windows

package main

// Windows speech, via the System.Speech synthesizer that ships with .NET
// Framework (the same SAPI5 voices the Narrator control panel lists).
//
// We drive it from a long-lived PowerShell process rather than a COM binding so
// that the whole program stays pure-stdlib Go and cross-compiles from any host.
// Each utterance is sent base64-encoded on stdin: the encoding is what keeps
// chat text out of PowerShell's parser, so nobody in earshot can inject code by
// typing it in-world. PowerShell acknowledges each line on stdout once the
// synthesizer has finished, which is what makes Speak block and the queue pace
// itself on real speech.

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// speakTimeout bounds one utterance. Messages are truncated well before this,
// so reaching it means the backend has wedged rather than that the line is long.
const speakTimeout = 3 * time.Minute

func powershell(script string) *exec.Cmd {
	return exec.Command("powershell",
		"-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodedCommand(script))
}

type winSpeaker struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	acks chan string
	opts SpeechOpts

	// readers tracks the two pipe-draining goroutines. Cmd.Wait closes the
	// pipes it created, so it must not be called until both have finished.
	readers sync.WaitGroup

	mu     sync.Mutex  // serializes Speak against itself
	closed atomic.Bool // checked without the lock so Close never waits on Speak
	reaped sync.Once
}

func newPlatformSpeaker(o SpeechOpts) (Speaker, error) {
	cmd := powershell(psSpeechScript)
	env := cmd.Environ()
	if o.Voice != "" {
		env = append(env, "SLTTS_VOICE="+o.Voice)
	}
	if o.Rate != "" {
		if _, err := strconv.Atoi(o.Rate); err != nil {
			return nil, fmt.Errorf("-rate must be a whole number from -10 to 10 on Windows, got %q", o.Rate)
		}
		env = append(env, "SLTTS_RATE="+o.Rate)
	}
	if o.Volume != "" {
		if _, err := strconv.Atoi(o.Volume); err != nil {
			return nil, fmt.Errorf("-volume must be a whole number from 0 to 100 on Windows, got %q", o.Volume)
		}
		env = append(env, "SLTTS_VOLUME="+o.Volume)
	}
	cmd.Env = env

	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting powershell for speech: %w", err)
	}

	s := &winSpeaker{cmd: cmd, in: in, acks: make(chan string, 8), opts: o}

	// PowerShell's own diagnostics go to stderr; relay them so they reach the
	// viewer log, and keep the pipe drained so it cannot fill and block.
	s.readers.Add(2)
	go func() {
		defer s.readers.Done()
		sc := bufio.NewScanner(errPipe)
		for sc.Scan() {
			o.logf("speech backend: %s", sc.Text())
		}
	}()
	go func() {
		defer s.readers.Done()
		defer close(s.acks)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			s.acks <- strings.TrimSpace(sc.Text())
		}
	}()

	// Wait for the backend to report a loaded voice, so that a missing
	// System.Speech assembly surfaces now rather than on the first message.
	select {
	case line, ok := <-s.acks:
		if !ok {
			_ = s.Close()
			return nil, fmt.Errorf("speech backend exited during startup (no System.Speech voice?)")
		}
		o.logf("%s", line)
	case <-time.After(20 * time.Second):
		_ = s.Close()
		return nil, fmt.Errorf("speech backend did not become ready within 20s")
	}
	return s, nil
}

func (s *winSpeaker) Speak(text string) error {
	if s.closed.Load() {
		return fmt.Errorf("speech backend is closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	enc := base64.StdEncoding.EncodeToString([]byte(text))
	if _, err := io.WriteString(s.in, enc+"\r\n"); err != nil {
		return fmt.Errorf("writing to speech backend: %w", err)
	}
	select {
	case _, ok := <-s.acks:
		if !ok {
			return fmt.Errorf("speech backend exited")
		}
		return nil
	case <-time.After(speakTimeout):
		return fmt.Errorf("speech backend stopped responding")
	}
}

// Close shuts the backend down. It does not take the Speak lock: closing stdin
// ends the script's read loop, which closes its stdout, which releases any Speak
// still waiting for an acknowledgement.
func (s *winSpeaker) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	_ = s.in.Close() // ReadLine returns null and the script leaves its loop

	var err error
	s.reaped.Do(func() {
		// Cmd.Wait closes the stdout and stderr pipes, so both readers have to
		// be done first; otherwise their reads can fail against a closed file
		// and the last diagnostics are lost.
		drained := make(chan struct{})
		go func() {
			s.readers.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			// Still speaking, or wedged. Killing it makes the pipes report EOF,
			// which lets the readers finish.
			_ = s.cmd.Process.Kill()
			<-drained
		}
		err = s.cmd.Wait()
	})
	// A killed process reports a non-zero exit; that is an expected shutdown
	// here, not a failure worth propagating.
	if err != nil {
		s.opts.logf("speech backend exited: %v", err)
	}
	return nil
}

func listVoices() (string, error) {
	out, err := powershell(psListVoicesScript).Output()
	if err != nil {
		return "", fmt.Errorf("listing voices: %w", err)
	}
	return string(out), nil
}

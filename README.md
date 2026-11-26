# sl-nearby-tts

[![ci](https://github.com/looprock/sl-nearby-tts/actions/workflows/ci.yml/badge.svg)](https://github.com/looprock/sl-nearby-tts/actions/workflows/ci.yml)

Reads Second Life **nearby chat** out of a running viewer and speaks it.

It runs as a LEAP plugin: the viewer launches it as a child process and streams
chat to it over a pipe. No bot account, no second login, no in-world object, and
no polling of chat logs. One self-contained binary with no runtime to install.

```
SecondLife --leap "'C:/Tools/sl-nearby-tts.exe'"
```

Those inner single quotes are not decoration — see [Quoting the --leap value](#quoting-the---leap-value).

## How it works

The viewer posts every nearby-chat message to an internal event stream named
`LLChat` (`LLFloaterIMNearbyChatHandler::sChatWatcher` in
`indra/newview/llfloaterimnearbychathandler.cpp`). A LEAP plugin can subscribe
to any named event pump, so this program asks to listen to that one, then speaks
what arrives:

```
viewer  --(stdout pipe)-->  sl-nearby-tts  --> SAPI / say / speech-dispatcher
        <--(stdin pipe)---
```

It also calls `LLAgent.getID` once so it knows your own avatar's UUID and can
skip reading your own messages back to you.

### What it can and cannot hear

| | |
|---|---|
| Nearby chat from avatars | yes |
| Chat from scripted objects, `llOwnerSay`, `llRegionSayTo` | yes |
| System and region messages | yes |
| Emotes (`/me waves`) | yes |
| **IMs and group chat** | **no** |

There is no incoming event pump for instant messages. The viewer's `GroupChat`
event API only sends (`startGroupChat`, `leaveGroupChat`, `sendGroupIM`), so
LEAP cannot read IMs at all. Reading those aloud needs a different mechanism —
tailing the per-conversation log files, or a bot client on the UDP protocol.

## Install on Windows

1. Download `sl-nearby-tts.exe` from the
   [latest release](https://github.com/looprock/sl-nearby-tts/releases/latest)
   and put it somewhere stable, e.g. `C:\Tools\`. Nothing needs installing
   alongside it.

   The binary is not code-signed, so the first run brings up "Windows protected
   your PC": choose **More info → Run anyway**. `SHA256SUMS` on the release page
   lets you confirm you got the file the build produced.

2. Check that speech works, before involving the viewer:

   ```
   C:\Tools\sl-nearby-tts.exe -selftest
   C:\Tools\sl-nearby-tts.exe -list-voices
   ```

3. Add the flag to the viewer's shortcut. Right-click the shortcut →
   Properties → Target:

   ```
   "C:\Program Files\Firestorm-Releasex64\Firestorm-Releasex64.exe" --leap "'C:/Tools/sl-nearby-tts.exe'"
   ```

   The single quotes inside the double quotes are required, and so are the
   forward slashes. Both are explained below.

4. Start the viewer from that shortcut and talk to someone.

### Quoting the --leap value

The value gets parsed **twice**, and getting either layer wrong means the plugin
simply never starts. Both the official viewer and Firestorm behave this way.

**First layer: it must be LLSD notation, not a bare path.** `--leap` maps to the
`LeapCommand` setting, which is declared `Type: LLSD`, so the viewer runs the
value through `LLSDNotationParser` (`LLControlVariable::setValueFromNotation`). A
bare path is not valid notation, the parse fails, and the code returns without
setting anything — there is no fallback to treating it as a string. Wrap it in
single quotes, which is how notation spells a string:

```
--leap "/Tools/sl-nearby-tts"      WRONG, never starts
--leap "'/Tools/sl-nearby-tts'"    right
```

When this layer fails, the viewer log says so:

```
WARNING LLControlVariable::setValueFromNotation : Failed to parse LLSD notation
for control 'LeapCommand': /Tools/sl-nearby-tts
```

**Second layer: backslash is an escape character.** What comes out of the
notation parser is then split into a command and arguments by
`LLStringUtil::getTokens(plugin, " \t\r\n", "", "\"'", "\\")` in `llleap.cpp`. So
a Windows path written with backslashes collapses to
`C:Toolssl-nearby-tts.exe`:

```
--leap "'C:\Tools\sl-nearby-tts.exe'"     WRONG, separators eaten
--leap "'C:/Tools/sl-nearby-tts.exe'"     right
--leap "'C:\\Tools\\sl-nearby-tts.exe'"   also right
```

**Options** go inside the notation string. Since that string is delimited by
single quotes, a value containing spaces uses double quotes:

```
--leap "'C:/Tools/sl-nearby-tts.exe -skip-objects'"
--leap "'C:/Tools/sl-nearby-tts.exe -voice \"Microsoft Zira Desktop\"'"
```

The no-options forms above are confirmed against the viewer's own parsers. If
nested quoting for an option value fights with your shell or your Windows
shortcut, put the whole invocation in a one-line `.bat` or shell script and
point `--leap` at that instead.

### Viewers

Works with the official Second Life viewer and with Firestorm; both ship
`llleap.cpp` and the `leap` command-line option. Other third-party viewers are
likely but unverified — if the plugin never speaks, search the viewer log for
`LLLeap`.

## Options

| Flag | Default | |
|---|---|---|
| `-voice NAME` | system default | see `-list-voices` |
| `-rate N` | system default | Windows `-10`..`10`, macOS words per minute, Linux backend-specific |
| `-volume N` | system default | `0`..`100`, Windows and Linux |
| `-queue N` | `6` | lines that may wait before the oldest are dropped |
| `-max-chars N` | `350` | truncate longer messages |
| `-name-window D` | `30s` | how long one speaker keeps the floor before being named again |
| `-dedup-window D` | `3s` | drop identical repeats inside this window |
| `-skip-objects` | off | ignore scripted objects |
| `-skip-system` | off | ignore viewer and region messages |
| `-speak-self` | off | also read your own messages |
| `-speak-debug` | off | read script errors |
| `-speak-urls` | off | read links aloud instead of saying "link" |
| `-self UUID` | auto | override the detected avatar ID |
| `-dry-run` | off | print to stderr instead of speaking |
| `-log PATH` | — | also append diagnostics to a file |
| `-selftest` | — | speak sample lines and exit, no viewer needed |
| `-list-voices` | — | list installed voices and exit |

### Choices that make it bearable to listen to

Speaking every event verbatim is unusable, so by default it:

- never speaks typing indicators, script-debug chat, or the backlog the viewer
  replays into the chat window on login (that last one would otherwise recite
  the entire window every time);
- says the speaker's name only when the speaker changes or after a 30s gap;
- drops `Resident` from names and reads a URL as "link";
- drops identical repeats within 3s, which is what gestures and relays emit;
- truncates a pasted wall of text instead of letting it block the queue;
- drops the oldest pending lines when it falls behind, because on a crowded
  parcel being current beats being complete.

## Diagnosing it on someone else's machine

Everything except the protocol itself goes to **stderr**, and the viewer copies
its plugin's stderr into the viewer log. So a user who cannot be walked through
a terminal can just send their log; look for `sl-nearby-tts:` lines. `-log
C:/Tools/sl-tts.log` writes the same diagnostics to a file as well.

Checks, in the order worth trying:

| Symptom | Check |
|---|---|
| `Failed to parse LLSD notation for control 'LeapCommand'` | the `--leap` value is missing its inner single quotes |
| no speech at all, nothing in the log | path in `--leap` has backslashes |
| `bad_protocol` or `LLLeap` errors in the log | wrong binary, or it is not a LEAP plugin |
| log says `subscribed to LLChat`, but nothing is read when I talk | your own chat is skipped by default; add `-speak-self`, or get someone else to say something |
| starts, but silent | `-selftest`; then `-dry-run` to see whether lines are arriving |
| wrong voice or too fast | `-list-voices`, then `-voice` / `-rate` |
| reads its own messages back | `-self <uuid>`; auto-detection needs the viewer to be logged in |

`stdout` is the protocol channel and nothing else may be written to it; a single
stray byte desynchronizes the viewer for the rest of the session.

## Building

Pure standard library, so every target cross-compiles from any host with no
toolchain setup:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o sl-nearby-tts.exe .
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -o sl-nearby-tts-macos-arm64 .
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o sl-nearby-tts-linux-amd64 .
```

Speech backends, picked at build time: Windows uses `System.Speech` driven by a
long-lived PowerShell process; macOS uses `say`; Linux uses `spd-say` or
`espeak-ng`. If no backend is available the program logs why and prints lines
instead of speaking, rather than refusing to start.

Chat text is handed to the Windows backend base64-encoded and decoded inside the
script, so it never reaches PowerShell's parser. That is a security boundary,
not a convenience: without it, anyone within earshot in-world could run code on
the listener's machine by typing it in chat.

## Testing without the target platform

```bash
go test ./...          # everything below
go test -race ./...
```

| Covered | How |
|---|---|
| LLSD notation codec | round trips, real viewer-shaped payloads, `\xNN` UTF-8, malformed input |
| Filtering and phrasing | table tests per rule |
| LEAP framing | length prefixes, short reads, desync, clean EOF |
| A whole session | `net.Pipe` mock viewer: handshake, subscribe, `getID`, chat, shutdown |
| The real binary | built and driven as a subprocess, which is what catches anything polluting stdout |
| Windows `-EncodedCommand` | base64/UTF-16LE decoded back in the test |
| PowerShell script invariants | asserts it decodes base64 rather than interpolating, acks every line, flushes, keeps diagnostics off stdout |

What that does **not** cover, and where it is covered instead:

- **SAPI actually making sound.** The `windows-speech` job in
  `.github/workflows/ci.yml` synthesizes to a WAV file on a Windows runner,
  which proves a voice is installed and synthesis works without an audio device.
  On a real machine, `-selftest` is the check.
- **The PowerShell process plumbing** (`speak_windows.go`). Compile-checked from
  any host with `GOOS=windows go vet ./...`, and exercised on the target machine
  by the opt-in test:

  ```
  set SLTTS_AUDIO_TEST=1
  go test -run TestRealWindowsSpeech -v
  ```

- **Windows stdio framing.** Go writes raw bytes on every platform, with none of
  the text-mode newline translation that would corrupt a length-prefixed stream
  in C or Python. The `test` matrix job runs the subprocess test on
  `windows-latest` to confirm it.

CI runs on every push to `main` and on pull requests, and can be started by hand
from the Actions tab. The `build` job uploads all five cross-compiled binaries
as an artifact.

Releases are cut by pushing a tag, which runs the checks again, cross-compiles
every target, and publishes the binaries with a `SHA256SUMS` file:

```bash
git tag -a v1.0.1 -m "what changed"
git push origin v1.0.1
```

`-version` on a released binary reports its tag; a local build reports `dev`.

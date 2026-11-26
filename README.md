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

### The wire format is not what the wiki says

The [LEAP page](https://wiki.secondlife.com/wiki/LLSD_Event_API_Plug-in) states
that messages are LLSD notation. That is out of date in one direction, and
getting it wrong means a plugin dies on the handshake:

| Direction | Format |
|---|---|
| viewer → plugin | **binary** LLSD, behind a `<? LLSD/Binary ?>` header |
| plugin → viewer | notation, and only notation |

`LLLeapImpl::wstdin` serializes with `LLSDSerialize::LLSD_BINARY`, citing
SL-18330 — binary parses far faster than notation for large payloads — and
`serialize()` prepends that header, so the first byte a plugin ever sees is `<`.
The viewer's read side still hard-codes `LLSDNotationParser`, so the reply
direction is unchanged. This program decodes both, dispatching on the header.

### What it can and cannot hear

| | |
|---|---|
| Nearby chat from avatars | yes |
| Chat from scripted objects, `llOwnerSay`, `llRegionSayTo` | yes, unless `-skip-objects` |
| Emotes (`/me waves`) | yes |
| System and region messages, including "X is online" notices | yes, unless `-skip-system` |
| **Your own messages** | **no, unless `-speak-self`** |
| **IMs and group chat** | **no, and not possible this way** |

There is no incoming event pump for instant messages. The viewer's `GroupChat`
event API only sends (`startGroupChat`, `leaveGroupChat`, `sendGroupIM`), so
LEAP cannot read IMs at all. Reading those aloud needs a different mechanism —
tailing the per-conversation log files, or a bot client on the UDP protocol.

Your own chat is skipped because hearing your own typing read back is grating in
normal use. It does mean that **testing alone sounds like nothing is working**:
add `-speak-self`, or get someone else to say something.

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

4. Start the viewer from that shortcut and talk to someone — or to yourself with
   `-speak-self`, since your own chat is otherwise skipped.

## Install on macOS and Linux

1. Download the binary for your platform from the
   [latest release](https://github.com/looprock/sl-nearby-tts/releases/latest)
   and make it executable:

   ```bash
   chmod +x ~/sl-nearby-tts-macos-arm64
   ```

2. **On macOS, clear the quarantine flag.** The binary is neither signed nor
   notarized, and Gatekeeper kills a quarantined unsigned binary when a GUI app
   such as the viewer execs it. That failure looks identical to the plugin
   simply never starting:

   ```bash
   xattr -d com.apple.quarantine ~/sl-nearby-tts-macos-arm64
   ```

3. Check speech works before involving the viewer:

   ```bash
   ~/sl-nearby-tts-macos-arm64 -selftest
   ~/sl-nearby-tts-macos-arm64 -list-voices
   ```

4. Launch the viewer with the plugin. The inner single quotes are required:

   ```bash
   /Applications/Firestorm-Releasex64.app/Contents/MacOS/Firestorm \
     --leap "'$HOME/sl-nearby-tts-macos-arm64'"
   ```

   Options go inside the same quotes, not after them:

   ```bash
   --leap "'$HOME/sl-nearby-tts-macos-arm64 -speak-self -skip-system'"
   ```

   On Linux, substitute your viewer's own executable; everything else is the
   same.

> The viewer launches the plugin **once, at startup**. After replacing the
> binary or changing its options, quit the viewer and start it again — there is
> no reload.

## Quoting the --leap value

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

## Viewers

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
- reads a system line as the sentence it already is: the viewer puts the name in
  `from` and only the predicate in `message`, so `Emilylo Mayako` + `is online.`
  becomes "Emilylo Mayako is online.", not "Emilylo Mayako says, is online.";
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

The log lives at `%APPDATA%\Firestorm_x64\logs\Firestorm.log` on Windows and
`~/Library/Application Support/Firestorm/logs/Firestorm.log` on macOS; the
previous session is in `Firestorm.old` beside it. A healthy startup looks like
this, and each line rules out a different failure:

```
processing --leap "'/path/to/sl-nearby-tts'"   the value parsed as LLSD
launched /path/to/sl-nearby-tts                the process actually started
version v1.0.2 connected; reply pump "…"       the binary handshake decoded
subscribed to LLChat                            the viewer confirmed it
own avatar id 557b8d88-…; own chat skipped      login finished, self detected
```

`subscribed to LLChat` is printed only when the viewer answers `status: true`,
so it is the viewer's word rather than an assumption. The `own avatar id` line
appears once you finish logging in, since `LLAgent.getID` returns a null UUID
before that and is retried every 15 seconds.

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

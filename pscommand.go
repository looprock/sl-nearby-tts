package main

// The PowerShell side of Windows speech lives here, with no build tag, so that
// the parts of it that are just string handling can be tested from any host.
// Only the process plumbing in speak_windows.go is Windows-only.

import (
	"encoding/base64"
	"unicode/utf16"
)

// psSpeechScript reads base64 lines on stdin, speaks each one, and acknowledges
// it on stdout so the caller can block until the synthesizer is finished.
//
// The base64 is a safety boundary, not a convenience: chat text never reaches
// PowerShell's parser, so nobody within earshot in-world can get code to run on
// the listener's machine by typing it.
const psSpeechScript = `
$ErrorActionPreference = 'Stop'
try {
    Add-Type -AssemblyName System.Speech
    $synth = New-Object System.Speech.Synthesis.SpeechSynthesizer
    $synth.SetOutputToDefaultAudioDevice()
} catch {
    [Console]::Error.WriteLine("init failed: " + $_.Exception.Message)
    exit 1
}
if ($env:SLTTS_VOICE) {
    try { $synth.SelectVoice($env:SLTTS_VOICE) }
    catch { [Console]::Error.WriteLine("voice '" + $env:SLTTS_VOICE + "' not installed, keeping " + $synth.Voice.Name) }
}
if ($env:SLTTS_RATE)   { try { $synth.Rate   = [int]$env:SLTTS_RATE }   catch { [Console]::Error.WriteLine("bad rate")   } }
if ($env:SLTTS_VOLUME) { try { $synth.Volume = [int]$env:SLTTS_VOLUME } catch { [Console]::Error.WriteLine("bad volume") } }
[Console]::Out.WriteLine("ready " + $synth.Voice.Name)
[Console]::Out.Flush()
while ($true) {
    $line = [Console]::In.ReadLine()
    if ($null -eq $line) { break }
    if ($line.Length -gt 0) {
        try {
            $synth.Speak([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($line)))
        } catch {
            [Console]::Error.WriteLine("speak failed: " + $_.Exception.Message)
        }
    }
    [Console]::Out.WriteLine("ok")
    [Console]::Out.Flush()
}
$synth.Dispose()
`

// psListVoicesScript prints the installed, enabled voices, which is how someone
// on the far end finds the name to pass to -voice.
const psListVoicesScript = `
Add-Type -AssemblyName System.Speech
$synth = New-Object System.Speech.Synthesis.SpeechSynthesizer
foreach ($v in $synth.GetInstalledVoices()) {
    if ($v.Enabled) {
        $i = $v.VoiceInfo
        [Console]::Out.WriteLine($i.Name + "  [" + $i.Culture.Name + ", " + $i.Gender + "]")
    }
}
`

// encodedCommand renders a script for PowerShell's -EncodedCommand, which takes
// base64 of UTF-16LE. Passing the script this way avoids both a temp file and
// every layer of Windows command-line quoting.
func encodedCommand(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

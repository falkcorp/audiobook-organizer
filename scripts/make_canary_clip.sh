#!/bin/bash
# file: scripts/make_canary_clip.sh
# version: 1.0.0
# guid: 05f5ccdc-fe39-44d2-8abd-62acd9e5b524
# last-edited: 2026-10-26
# Regenerate internal/transcribe/canary_clip.wav, the canary clip the dispatcher
# sends a faulted machine (see internal/transcribe/canary.go). Needs espeak-ng
# (its output is not covered by its licence) and ffmpeg. With eSpeak NG 1.52.0
# the output is byte-identical: sha256 87e6febe...4717f.
set -euo pipefail
out="${1:-internal/transcribe/canary_clip.wav}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
espeak-ng -v en-us -s 130 -g 4 -w "$tmp/raw.wav" "Welcome to Gotham City."
ffmpeg -hide_banner -loglevel error -y -i "$tmp/raw.wav" \
	-af "silenceremove=start_periods=1:start_threshold=-50dB,areverse,silenceremove=start_periods=1:start_threshold=-50dB,areverse,apad=pad_dur=0.2" \
	-ar 16000 -ac 1 -c:a pcm_s16le -map_metadata -1 -fflags +bitexact -flags:a +bitexact "$out"

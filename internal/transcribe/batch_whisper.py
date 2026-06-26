#!/usr/bin/env python3
# file: internal/transcribe/batch_whisper.py
# Loads whisper model ONCE, transcribes all jobs, outputs JSON to stdout.
# Called by TranscribeBatch in batch.go — never invoke directly.
# Usage: python batch_whisper.py <model> <jobs.json>
#   jobs.json: {"<id>": "<wav_path>", ...}
#   stdout:    {"<id>": {"text": "...", "error": null}, ...}
#   stderr:    progress / device info
import sys, json, warnings
warnings.filterwarnings("ignore")

import torch
import whisper

_, model_name, jobs_path = sys.argv

with open(jobs_path) as f:
    jobs = json.load(f)

device = "cuda" if torch.cuda.is_available() else "cpu"
# Log to stderr so it doesn't contaminate the JSON stdout output.
print(f"[batch_whisper] device={device} model={model_name} jobs={len(jobs)}", file=sys.stderr, flush=True)

model = whisper.load_model(model_name, device=device)

results = {}
for i, (book_id, wav_path) in enumerate(jobs.items(), 1):
    try:
        r = model.transcribe(wav_path, language="en", task="transcribe", fp16=(device == "cuda"))
        results[book_id] = {"text": r["text"].strip(), "error": None}
    except Exception as e:
        results[book_id] = {"text": "", "error": str(e)}
    if i % 10 == 0 or i == len(jobs):
        print(f"[batch_whisper] {i}/{len(jobs)} done", file=sys.stderr, flush=True)

json.dump(results, sys.stdout)

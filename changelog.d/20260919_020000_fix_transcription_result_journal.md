### Fixed

- Restarting or redeploying the server while book intros are being
  transcribed no longer throws away transcripts that were already finished.
  Each transcript is saved the moment it comes back from the transcription
  server, and the next run picks it up instead of sending the audio again.
- Running the intro transcription again over books that already have the same
  transcript no longer rewrites them, and books that share an identical intro
  clip (such as the same publisher opening) are transcribed once but every one
  of those books still gets its transcript.
- Saved transcripts are tied to each transcription server's settings, and a
  "retry silent books" run always asks the server again, so tuning the server
  to hear quiet intros is never undone by an old saved "silent" answer.

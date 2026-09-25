<!-- file: .claude/notes/test-audiobooth-swift-decode-proof-progress.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9a3e71c2-6b58-4f0d-a2e4-5c1d8f93b7e0 -->
<!-- last-edited: 2026-09-25 -->

# test/audiobooth-swift-decode-proof progress

Done:
- Step 1: proposed-main has Go gap tests (abs_decode_gaps, audiobooth_*gaps) and the F1-F6
  routes; no Swift harness exists. Old scratch harness is gone.
- Pinned AudioBooth@849eac04 in tests/audiobooth-decode/audiobooth.pin.

Next:
- manifest.json (45 call sites), Go fixture generator, Swift package, prep script, make target.

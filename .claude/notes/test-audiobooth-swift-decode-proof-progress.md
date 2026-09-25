<!-- file: .claude/notes/test-audiobooth-swift-decode-proof-progress.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9a3e71c2-6b58-4f0d-a2e4-5c1d8f93b7e0 -->
<!-- last-edited: 2026-09-25 -->

# test/audiobooth-swift-decode-proof progress

Done:
- Step 1: proposed-main has Go gap tests (abs_decode_gaps, audiobooth_*gaps) and the F1-F6
  routes; no Swift harness exists. Old scratch harness is gone.
- Pinned AudioBooth@849eac04 in tests/audiobooth-decode/audiobooth.pin.
- manifest.json (45 call sites + extras), Go replay test, Swift package, prep script,
  `make audiobooth-decode`. Swift: 2 tests pass.
- Coverage: 35/45 decoded, 6/45 2xx-only, 1/45 designed error, 3/45 N/A (podcast), 0 failed.
  Models 19/26 decoded with data; 3 empty (EreaderDevice, ListeningHistorySession,
  SessionSync), 4 N/A (Connection, Podcast, PodcastEpisode, RecentEpisode).
- Real bug fixed: base64 filter values containing '+' (URLComponents leaves '+' literal)
  served an empty page. absFilterGroup maps space back to '+'.

Next:
- make ci, final push, report.

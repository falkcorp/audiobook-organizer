### Fixed

- **AudioBooth's narrator filter no longer shows an empty list for some names.**
  The app sends filter values as base64 and leaves a `+` in the query as a
  literal `+`, which the server read as a space. Any name whose base64 contains
  a `+` (for example "Seán O’Brien") failed to decode, and the filter showed
  "no books". Every filter group shares that decoder, so a genre or language
  value whose base64 contains a `+` was affected the same way.

### Added

- **A decode proof against the AudioBooth app's own Swift models.**
  `make audiobooth-decode` fetches the AudioBooth commit pinned in
  `tests/audiobooth-decode/audiobooth.pin` into a gitignored cache, replays every
  request the app makes against our ABS handlers, and decodes each response
  through the app's model types with `swift test`. No AudioBooth code is
  committed. The Go replay (`TestAudioBoothFixtures_ReplayEveryAppRequest`) also
  runs in `make ci` without Swift. Coverage: 35 of 45 app call sites decoded,
  6 of 45 checked for a 2xx where the app does not decode, 1 of 45 answered with
  its designed error, and 3 of 45 podcast-only call sites not applicable.

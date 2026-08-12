## ABS

- [ ] **Align the ABS conformance fixtures with the oracle capture so the value gate can be
      turned on permanently.** `assertConformant` still runs with `CompareValues` off, so no test
      compares a single value. Turning it on today reddens **12** tests — but reading the findings
      rather than counting them shows they are mostly *not* defects:

      - **Fixture drift (most of them).** The fake library seeds a synthetic book; the oracle is a
        real capture of *The Odyssey*. So `size` is 4096 vs 1.20828875e8, `duration` 9975 vs
        9975.480544, `publishedYear` `800` vs `800BC`, track titles `The Odyssey: Book 06` vs
        `odyssey_06_homer_butler_64kb.mp3`, `timeBase` `1/1000` vs `1/14112000`.
      - **Deliberate divergences** that must be whitelisted, never "fixed": `user.type` is `user`
        not `root` (`dto.go:275-277` — it makes Absorb hide the admin UI we do not implement), and
        `Source` is `audiobook-organizer` not `docker`.
      - **Two worth an actual decision:** whether `media.tracks[].title` should be the filename
        (as ABS sends) rather than a display title, and the author ordering in `/personalized`.

      The work is to seed the fake library FROM the oracle fixture so the values match by
      construction, then flip the gate on and keep it on. `library_fake_test.go` is 767 lines, so
      this is bounded but not small.

      ⚠️ **Do not chase green by normalizing `size`/`duration`/`progress`/`currentTime`/
      `startOffset`.** `normalize.go:19-20` records keeping them comparable as an explicit
      decision — they are real playback data. Normalizing them would make the suite pass while
      deleting the exact signal the gate exists to produce. Four environment-dependent keys
      (`fullpath`, `loadedat`, `ipaddress`, `useragent`) have already been normalized, which is
      what took the count from 13 to 12; that is the end of what normalization can honestly fix.

      Also still open from the same audit: 4 golden fixtures that no test ever loads.

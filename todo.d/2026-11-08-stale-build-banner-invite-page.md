- [ ] **PLATFORM-CORE-R1-19-BANNER-INVITE-PAGE** Decide whether the invite page
      (`/invite/:token`) needs the stale-build banner. R1-19 mounts
      `StaleBuildBanner` once in `App`, after the invite branch's early return,
      because that page is public and stands alone; a tab left open on it across an
      upgrade is not offered a reload. Mount the banner above that branch if the
      page should offer it.

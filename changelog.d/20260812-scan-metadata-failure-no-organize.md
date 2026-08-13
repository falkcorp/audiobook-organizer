### Fixed

- A scan whose metadata pass failed logged the error and then **carried on into
  auto-organize anyway**. Those books still held whatever title/author the scan
  started with — frequently empty — so organizing them expanded the naming pattern
  over blank fields and sent them all to the same degenerate path. In the 4h15m
  production scan of 2026-08-11, 848 books collided on
  `Unknown Author/Unknown Title/Unknown Title - Unknown Author.mp3`. The failure now
  skips auto-organize for that folder and returns, so the folder is also no longer
  counted as a successful scan.

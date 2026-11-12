### Fixed

- core/tests: `TestScrub_HostileBodyIsBounded` still flaked after the growth-ratio rewrite. A 4x input step left too little room between linear code under `-race` noise (9.8x was seen for the `Bearer` shape on CI) and quadratic (16x). The linearity check now compares 32 KiB against 1 MiB: linear costs about 32x, quadratic about 1024x, and the bound is 128x. `scrubber.text` also sizes its output builder once instead of regrowing it across a large body.

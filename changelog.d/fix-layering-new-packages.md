### Fixed

#### The layering test passes on main again

`internal/serverdecode` and `internal/telemetry/contract` arrived in PRs that were
tested before the layering test merged. Main then failed `TestLayering` because the
two packages had no layer. Both import no module package, so both are layer 0.

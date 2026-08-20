<!-- file: changelog.d/20260820_152000_review_lane_from_url.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9928ed4d-14df-425e-98e1-21462aed7056 -->
<!-- last-edited: 2026-08-20 -->

### Fixed

#### `/review` deep links open the lane they are about

The workspace always opened on the metadata lane regardless of the URL, which made
the dupes lane's own entry point unreachable: a `?book=` link arrived, metadata
opened, and because each lane only fetches while it is the visible one, the
server-side entity filter that link exists to trigger never ran.

`/review` now reads the lane from the URL — `?lane=` when named, otherwise inferred
from `?book=` / `?band=`, falling back to metadata. The lane is seeded from the URL
at mount rather than mirrored to it, so switching lanes still costs exactly one
fetch.

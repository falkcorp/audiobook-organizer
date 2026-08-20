<!-- file: changelog.d/20260820_182500_review_staleness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c05e2f1-4a76-4b93-9d28-1f6b0a3c7e54 -->
<!-- last-edited: 2026-08-20 -->

### Fixed

#### The review lane now shows how old a cached candidate is

The metadata cache has a 30-day TTL whose stated contract is that older entries
stay readable and the UI flags them and offers a refresh. The review listing
sent no timestamp at all, so there was nothing to flag with — and on the live
library **10,949 of 10,952** cache entries are past that TTL, the oldest fetched
three months ago. Every one of them was presented as though freshly fetched.

Review rows now carry `fetched_at` and `is_fresh`, the rail shows how many of
the rows in front of you are stale, and each stale row is marked with its actual
age. Nothing is hidden or dropped: staleness is a caveat on the list, not a
shortfall.

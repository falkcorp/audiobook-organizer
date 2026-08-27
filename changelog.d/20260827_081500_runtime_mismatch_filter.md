<!-- file: changelog.d/20260827_081500_runtime_mismatch_filter.md -->
<!-- version: 1.0.0 -->
<!-- guid: 687b5924-8f07-4ad4-bc4c-d17c5cfc2ee1 -->
<!-- last-edited: 2026-08-27 -->

### Added

#### Metadata review — hide known runtime mismatches

Reviewers can now hide candidates whose known runtime differs materially from the
library book. The filter starts off, and candidates with no duration information
remain visible so missing metadata is never treated as a matching runtime.

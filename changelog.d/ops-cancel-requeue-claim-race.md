### Fixed

- Canceling an operation whose row was just re-queued (a stand-down release or a retry won the race against the cancel) could overwrite the status of a run that a worker had claimed in between: the row said "canceled" while the run kept going, and its own terminal write then replaced "canceled". `Registry.Cancel` now cancels a row it reads as queued with the same compare-and-set the worker uses to claim it, and when that loses because the run has started, it cancels the live run instead. The `retryMu` comments now list Cancel among the paths that hold it and say the dispatcher and workers do not.
- The Review > Repairs page no longer says canceled rows "were not written". A canceled apply can stop partway through a multi-step row, so those rows are now labeled "Canceled: not applied, or stopped partway", the banner says applying them again finishes them, and the count reads "canceled before finishing".

### Added

- A test pins that the run fence is bound to the run context's Done channel at the point `executeRun` builds it, so a def timeout or parent cancel fences the run the instant the context ends.

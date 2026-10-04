### Fixed

#### The owner-manual-only gate test checks each field again

`TestEvaluateTranscribed_OwnerManualOnly` gave every case the candidate author "Big Finish Productions". Once #3727 started checking the candidate author, every bulk case was held by that one check, so removing the path check or the candidate-series check from `ManualOnlyDetail` still passed the package. Each case now defaults to a neutral author, names the one field that should hold it, and asserts that field in the refusal detail. Removing either check now fails its case.

The bulk-apply control for ordinary books now also includes a non-Doctor Who BBC Audio book. When a caller's wait for an import-path read times out, the Warn now says whether a list arrived in the meantime, instead of always saying "no import roots".

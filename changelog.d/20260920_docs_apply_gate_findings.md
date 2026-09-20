### Changed

- Filed three findings from a census of `metadata.batch-apply-cached` gate
  refusals: the applier re-picks books whose refusal cannot change between runs,
  `transcription_mismatch` reports neither side's values where `runtime_mismatch`
  beside it reports both, and ten books hold their entire file set twice at
  exactly 2.00x runtime. Tracking entries only — no behaviour change.

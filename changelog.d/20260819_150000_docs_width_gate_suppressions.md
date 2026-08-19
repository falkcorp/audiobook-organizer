### Changed

#### The interface-width baseline now records what a count of 0 does not mean

`.interface-width-baseline` holds `0` and documents at length how the number can
lie, but never stated that the threshold is `max: 8` or that two declarations
are over it today under explained `//nolint:interfacebloat` directives —
`BookReader` (10 entries) and `ServerDeps` (14). Read without that, `0` means
"nothing is wide," which is not what it measures.

Both suppressions are intentional and both are currently off-limits: `BookReader`
by the standing decision to migrate consumers to the ten pieces rather than
shrink it in place, `ServerDeps` because it sits in the hands-off missing-file
repair lane.

Recorded because the omission actually misled a reader, and the ad-hoc script
written to double-check it was also wrong — it reported `TranscriptionRunners`
at 12 entries when it has 2, counting each line of a multi-line parameter list
as a separate entry. golangci-lint is the authority on declared-entry counts.

No behaviour change; the gate still reports `baseline=0 actual=0`.

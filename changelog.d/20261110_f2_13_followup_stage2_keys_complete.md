### Changed

#### R2 stage-2 corpus test checks that each case's key list is complete (F2-13 follow-up)

`TestR2CorpusStage2_StoresHoldTheirCases` now diffs every stage-2 store against stage 1 and asserts that the rows it adds or changes are exactly the union of its cases' `keys` (down to the settings blob's fields), and that the rows it drops are exactly the union of their `removed_keys`. Before, it checked only that each listed key existed, so a key deleted from a case's list passed both test suites. No row is exempt; there are no background writes in today's stores.

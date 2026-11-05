### Fixed

- Fixed flaky transcribe `TestChangedHands_*` tests (16 of them, including `TestChangedHands_APortTakenOverByAnotherMachineDuringTheWaitIsDroppedNotStoredAsTheOldMachine`, which failed on a loaded CI agent). They changed a port's machine id and ended its cooldown on wall-clock timers started with the test, so an agent slow to begin the dispatch ran both before the gate read the port; the port then looked like a healthy worker of another machine and took every job. Those events are now anchored to the dispatch's first gate. Test-only change.

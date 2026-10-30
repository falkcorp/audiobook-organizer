<!-- file: docs/decisions/2026-10-30-whisper-midrequest-identity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2a27d1d3-6e7a-475e-8ae1-383463e0789c -->
<!-- last-edited: 2026-10-30 -->

# Whisper worker swapped for another machine mid-request

Implements the TODO.md entry TODO-WHISPER-MIDREQUEST-IDENTITY. Code: `internal/transcribe/workerswap.go`, `liveness.go`, and the two `send` call sites in `dispatcher.go`.

## Decision log

1. **The frozen identity reaches the poller through the request context, not a parameter.** `withFrozenWorker` is set where the dispatcher calls `send` (the pool round loop and `dispatchSingle`); `pollWorkerLiveness` reads it. Rejected: adding the key to `superviseRequest` / `transcribeRemoteWithHealth` / `transcribePathChunks` and the `TranscribeByPathBatch` sender closure. That threads a new argument through every transport and edits `TranscribeByPathBatch`, which PR #4285 is changing. A request sent without the value (any other caller) is simply not checked.
2. **The comparison reuses `reprobed` and `resolveMachine`, the rules `regate` applies after a cooldown wait.** The pool rule is "any identity different from the frozen one", the lone endpoint additionally keeps a worker that gained its machine id (`lone`). A frozen identity of "" or "?" has nothing to compare. A worker that missed its id and answers "u:" resolves to the machine last seen at the URL, so it is not a swap; with no such link it stands for itself and is one, exactly as at the gate. Rejected: a looser rule for mid-request (treat any "u:" as unchanged). It would let a non-id-capable worker that took the URL over keep the old machine's name.
3. **The swap check runs before the stuck check in the poll.** A decode past its deadline on the new machine says nothing about the old one; checked after, it would be recorded as a stuck request of the old key (`soloStuck`), which is the misattribution this fixes.
4. **A new error kind, `WorkerSwappedError` / `ErrWorkerSwapped`, that does not wrap the request's own error.** The cancelled call's error is a `*url.Error`, which `isTransportishError` reads as an outage the ops count toward stopping a run; the new kind is excluded there as worker faults are, and its text avoids every transport wording. `Is(errPortChangedHands)` is true so the pool-emptied and lone-endpoint deferrals read the same as a port found changed after a wait.
5. **The whole request is withheld.** The dispatcher stores none of the sender's results for a swapped request, earlier chunks included: they cannot be told from the swapped-in machine's. A response that raced the poll's cancel (the call returned nil after the cause was set) is withheld too. Rejected: keeping earlier chunks. The swap could have happened before the first poll saw it.
6. **No bench, no recorded failure, port dropped from the dispatch.** No `markEndpointFailure` (it is process-wide and the port is not down), nothing in `lastErr`, `soloStuck`, `faultedOn`, sick or canary state. The jobs stay in `remaining` and go to the other ports; the port gets a `ChangedHands` refusal and is dropped through `heldDrops`, applied at the next round start, because dropping mid-round changes `soloLimit` under answers still being judged. No process-wide "re-probe before use" flag is set: the entry asks for the port to be dropped and the next dispatch's gate to key it, and a flag would need new shared state. A pool emptied this way ends as `errPortChangedHands`. The lone endpoint defers its jobs with the same error.

## Residuals

- A swap between the last poll and the end of a request (up to `livenessInterval`, 30 s) is not seen.
- Workers older than whisper_mlx_server.py 1.8.0 (no `busy_seconds`) are never polled, so they are not checked.
- The result journal keys on URL and model, not machine: results of chunks that completed before the swap was seen stay journalled for that URL. Not changed here (`journal.go`, `remote.go` and `path.go` are outside this surface).
- The poll reads the canary memory (`byURL`) and never writes it; a concurrent dispatch's refused probe of the same URL can end the link, so a "u:" can then read as a swap. That costs a re-dispatch of the jobs elsewhere, never a wrong strike.

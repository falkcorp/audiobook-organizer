### Fixed

- `TestPool_TheCanaryDecidesWhichFaultedMachinesAreBelieved` ("canaries = 5,
  want 6", "canaries = 3, want 4") and `TestPool_ALooperPageThatFaultsEveryEndpointConvergesOnPerClipVerdicts`
  ("the scenario must fault every endpoint in round 1: 5 of 6") no longer fail
  intermittently on Linux CI. The whisper endpoint cooldown is process-wide
  state keyed by URL, a cooldown is 30 seconds per consecutive failure, and the
  tests' httptest servers were never cleared of it, so a server that was handed
  a closed server's port started benched. The dispatcher sent that port no work
  in the first round, so it never faulted in that dispatch and the canary rule
  (which asks the ports that faulted) did not cover it. `useEndpoints` now clears
  the cooldown of the URLs it configures at the start of a test and when it
  ends, and `TestUseEndpoints_ClearsACooldownLeftOnAReusedPort` pins it. No
  production code changed.

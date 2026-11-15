### Fixed

#### `TestVerify_APollProbeDuringTheRequestVouchesForTheResponse` — no longer flaky under -race

The test let the request answer, and took `/health` down, as soon as the stub
server's handler had been entered. The handler counts a probe before it chooses
its status, so the probe being counted could itself become a 500, and the poller
records a confirmation only after the client has read the 200, so the
post-response check could run before any evidence existed. Either interleaving
withheld the response as unverified and failed the test once in CI. The request
now waits until the poll's confirmation is recorded before it answers. Test-only;
no production behaviour changed.

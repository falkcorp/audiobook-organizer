### Fixed

#### Cloudflare Access sign-ins no longer try to create an old-style user under core identity

Under core identity, core's Access extension signs in a Cloudflare Access request. The app's older Access middleware still ran in front of the API, the metrics endpoint and the event stream, and on every Access request it tried to find or create a user in the old user records, which the identity cutover empties and closes to writes. That attempt was always refused, so it bound no one, but its outcome depended on the store refusing it. Under core identity the middleware now only verifies the Access token and records that it verified, which the client-address rule still reads. It reads and writes no user records. Without core identity it behaves as before.

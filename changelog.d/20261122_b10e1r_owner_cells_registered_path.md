### Added

- Registered-path owner cells for the identity flip (F4-08 B10e1r): the nine owner
  routes are asserted through the booted flipped build, with the owner set and the
  Cloudflare Access verifier, and with the owner set and no verifier (every principal
  of the table refused, as HEAD does). Also the Access admission policy (an address
  off the allow-list creates nothing, an allow-listed non-owner is never
  owner-verified, even as an admin) and the shared-address install (two administrators
  with the owner's address: the Access sign-in lands on the linked one and neither is
  merged or displaced). Test-only; no runtime change.

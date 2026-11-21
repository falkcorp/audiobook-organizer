### Added

- internal/cfaccess: `ClientAddrTrust.AccessClientAddr`, the "local peer + verified Access assertion" client-address
  rule in net/http form, for the identity flip (F4-08 A8, dark). Nothing calls it yet; the gin copy in
  `client_trust.go` stays authoritative until the switch, and a parity test keeps the two in step.

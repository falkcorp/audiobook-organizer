- **Fixed:** `maintenance.missing-file-repoint` declared no `Liveness`, so the server
  refused to start. The op now declares `LivenessRunItems`, matching its two siblings.
- **Fixed:** the maintenance plugin's def guard hand-copied a subset of the registry's
  registration rules, so it could only ever catch clauses its author had enumerated.
  `Registry.RegisterOp`'s stateless checks are now exported as `registry.ValidateOpDef`
  and the guard calls that, making the unit test equal the boot check by construction.

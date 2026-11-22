### Added

- Core's `InviteAccepted` lifecycle event now carries `Username` (the name the created account was written with) and `AcceptedAt` (the accept's own clock reading, equal to the account's `CreatedAt`) (F4-08 B10x2). Additive: no existing field changed. Books' accepted-invite row uses them instead of reading the created account back from core's identity view.

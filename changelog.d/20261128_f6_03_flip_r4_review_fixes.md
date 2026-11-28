### Changed

The log tail (`core.log.recorded.v1`) and the six event-stream and outbox operator routes (`GET /api/core/v1/events/topics`, the outbox consumers, dead-letter list, retry, discard and backlog purge) now require every core scope of the settings.manage atom, reads and writes alike, where an earlier cut of this flip required `core:system.view` or `core:system.manage` alone. A custom role holding only `core:system.manage` is refused on all of them. With authentication off these routes stay open to the auth-disabled principal, as the settings.manage routes were; the purge still needs an interactive sign-in.

The task executor is built only when the tasks flip is on or a module registers an outbox handler, so an install on the old operations recorder never has two owners of the `audiobook_organizer.ops.*` metric families. The books relocation follow-up (outbox consumer, `SetRelocationOutbox`) is wired only when both the identity and the tasks flips are on, and a build that wires it without an outbox installed refuses to boot.

A new task permission, `Perms.OutboxSystemStart`, lets a module's own outbox consumer start a kind whose `Start` names a scope no system actor can hold; the kernel checks every such consumer at boot (P7). Nothing else honours it.

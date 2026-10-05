- [ ] **AUTH-SNAPSHOT-MEM** `authority.LoadSnapshot` retains about 1 KB per
      person (about 78 MiB for 80,000 persons, about 199 MiB allocated while
      loading; measured 2026-10-05 by the opt-in
      TestSnapshot_MemoryAtRealisticSize). Most of it is the per-entry role
      and tier maps. Before the first bulk consumer loads one, consider a
      compact entry (fixed role/tier arrays, interned sources).

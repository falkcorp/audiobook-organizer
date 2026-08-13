## BUG: 10,780 version groups elect MORE than one primary

Found 2026-08-13 while repairing the opposite defect (groups electing *no*
primary, fixed by `ElectMissingPrimaries`). A full per-group census of all
63,870 books found:

| shape | groups |
|---|---|
| exactly one primary | ~13,530 |
| **more than one primary** | **10,780** |
| zero primaries | 479 (repaired) |

So the multi-primary shape is not an edge case — it is nearly half of all
version groups.

**The member-count histogram is the lead.** Of the 10,780 groups: 9,824 have
exactly 3 members, 842 have 2, 110 have 4, 4 have 5-6. A single shape repeating
~9,800 times is a systematic writer with a rule, not data drift.

**Some groups contain genuinely different books**, which means this is not only
a flag-accounting bug — the grouping itself is wrong:

```
group 01KNDCGCM62AGA9GYGV3G0523J  members=3 primaries=2
   primary=True   The Boxcar Children Collection, Volume 2
   primary=True   Mike's Mystery              <- a different book
   primary=False  The Boxcar Children Collection, Volume 2

group vg-67868a6ffc2aa170  members=4 primaries=2
   primary=True   Singularity Online Book 2
   primary=True   - Sorcerer Ascendant (2020)  <- title is a bare subtitle
   primary=False  Singularity Online Book 2
   primary=False  - Sorcerer Ascendant (2020)
```

Note the second group's id is `vg-67868a6ffc2aa170` — 16 hex chars, **not** the
`vg-<ULID>` shape the current code mints. That is a third, older id format and
probably identifies the writer responsible.

### Steps

1. Identify the writer(s) minting 16-hex-char `vg-` ids. The id format is the
   cheapest available fingerprint — bucket the 10,780 groups by id shape first
   and see whether multi-primary correlates with one shape.
2. Decide whether the fix is demotion (too many primaries) or **regrouping**
   (books that should never have shared a group). The Boxcar Children sample
   says at least some are the latter, and demoting a primary there would paper
   over a wrong group rather than fix it.
3. **Do not write a blind demotion pass.** Unlike electing a missing primary —
   which strictly increases visibility and is safely reversible — demoting a
   primary can hide a book that is currently visible. Any apply needs a dry run
   reviewed against real samples first.
4. Extend the invariant to both directions once the cause is known: every
   version group elects **exactly** one primary. The zero-primary half is
   already asserted in `TestVersionGroupInvariant_ZeroPrimaryGroupsAreRepaired`.

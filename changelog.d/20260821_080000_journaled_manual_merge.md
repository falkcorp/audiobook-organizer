<!-- file: changelog.d/20260821_080000_journaled_manual_merge.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a02ac0a-1371-4ff9-9e99-7fc05592129a -->
<!-- last-edited: 2026-08-21 -->

### Fixed

#### A merge you trigger by hand is now as reversible as one the system makes

`Engine.UnmergeAuto` reverts both books to their pre-merge snapshots, and it
only ever worked for Tier-1 auto-merges. The journal it reads from was written
in exactly one place — `autoMergeCertain` — so a merge dispatched from the
review lane recorded no pre-merge snapshot timestamps at all. There was nothing
to revert *to*.

That put the guarantee precisely backwards. The merges the system makes on its
own, after scoring a pair as CERTAIN, were the reversible ones. The merges a
person triggers by keystroke while triaging a queue — faster, less deliberated,
and far more likely to be a mistake — were the ones with no undo.

The sequence now lives in `Engine.MergeJournaled` and both paths share it:
capture the pre-merge `book_ver` baselines, write a provisional journal entry,
merge, then patch the same key with the authoritative winner, loser, and
snapshot timestamps. A provisional-write failure remains a **hard** error that
skips the merge, because an irreversible merge with no undo key is a worse
outcome than no merge at all — and that invariant now protects both callers
instead of one.

The review-lane endpoint refuses with 503 when the dedup engine is unavailable,
rather than falling back to the un-journalled merge service. Silently making a
merge irreversible is the failure mode this exists to prevent, so it is stated
rather than hidden, matching how every other engine-dependent endpoint on that
handler already behaves.

This closes the first of the three gaps recorded under `MERGE-UNDO`. The other
two — reversing the external-ID reassignment, and an endpoint to invoke the
undo — are unchanged and still open. Journalling is the prerequisite for both:
neither can be built against merges that recorded nothing.

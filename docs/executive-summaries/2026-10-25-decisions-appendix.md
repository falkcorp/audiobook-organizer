<!-- file: docs/executive-summaries/2026-10-25-decisions-appendix.md -->
<!-- version: 1.0.0 -->
<!-- guid: e75847e4-760a-4d0d-8527-1eeffece31f9 -->
<!-- last-edited: 2026-10-25 -->

# Appendix: decisions taken on the owner's behalf, 2026-10-25

From 2026-10-25 12:53 EDT to 2026-10-26 12:53 EDT (or until you say otherwise), every question gets its **Recommended** answer without waiting. Each one is logged here and in the executive-summary appendix (`docs/executive-summaries/2026-10-25-decisions-appendix.md`, through a docs PR). Overturn any of them by number.

| # | When | Question | Options considered | Taken (Recommended) | Why |
|---|---|---|---|---|---|
| D1 | 10-25 earlier, your answer | Dynamic collections: whose state decides membership by read status/progress/last played? | Per viewer · Owner's state · Shared | **Per viewer** (you chose) | Another user's listening data is admins only |
| D2 | 10-25 earlier, your answer | Bulk trash/restore permissions | Match the single-book permissions · Admins only · Unchanged | **Match single** (you chose): soft delete needs library.delete, restore needs library.organize | Same rule as deleting one book |
| D3 | 10-25 12:53 | Hash backfill: 515k files with no hash; the sampled failures were all files missing on the server. Skip known-missing files? | Skip known-missing and report the count · Keep retrying every run · Delete the rows | **Skip known-missing and report the count** | Retrying files that aren't there wastes the run; book_file rows are never deleted |
| D4 | 10-25 12:53 | #4140 purge: leftover file records when the trash is empty. Replay at boot, or a button in the purge dialog? | Dialog button · Boot replay · Both | **Dialog button** (worker's choice, under review) | At boot an unmounted drive reads as empty, so a replay would treat the files as gone and clear their records |
| D5 | 10-25 12:53 | #4140: `DiscardProgressAndPurge` still deletes the row before checking the file. Reorder it the same way? | Fix in #4140 · Follow-up PR · Leave | **Fix in #4140** | Same defect class as B-2, fix it properly |
| D6 | 10-25 12:53 | E2E invite test: how to receive the email | Cloudflare Email Routing catch-all on jdfalk.com · Dedicated Gmail over IMAP | **Cloudflare catch-all** | No new account, scriptable |
| D7 | 10-25 12:53 | #4155: per-user `unstarted`/negated filters read library-scale state. Add a per-user prefix-scan store method? | Add the method · Accept the scan cost | **Add the method** (pending the review's numbers) | Keeps collection reads from scaling with library size |

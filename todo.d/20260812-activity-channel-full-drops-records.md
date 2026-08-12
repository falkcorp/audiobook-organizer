## ⚠️ The activity channel overflows during organize and DROPS records

While accounting for organize failure logs on production, 1,000 of the 19,519 matching
lines since 2026-08-10 turned out not to be organize failures at all:

```
[WARN] activity channel full, dropped: operation: …
```

They cluster tightly — 44–70 per second during the Aug 11 22:35–22:36 organize run — i.e.
the activity pipeline saturates precisely when an operation is producing the most activity
worth recording. Every dropped line is an activity record that no longer exists anywhere.

**Why it matters.** The drop is announced only in the process log, at WARN. Nothing in the
API, the activity feed, or the operation summary tells a user their activity history has
holes in it, or where. Anyone reading the activity log for "what did this organize run
do?" gets a silently truncated answer that looks complete — the same shape as the other
silent-success defects on this list.

**What is NOT measured:** whether the drops are uniform or biased toward a particular
activity type; whether an operation's own change rows (`organize_failed` /
`organize_summary`) go through this channel and are therefore also lossy, or use a
different, durable path. Establish that first — if operation changes are lossy, then the
per-book error detail an organize run is supposed to leave behind is unreliable, which
undermines using it as evidence for anything else.

**Fix directions, cheapest first:**

1. **Count and surface.** Keep a dropped-record counter and report it on the operation and
   in the activity feed ("N activity records dropped during this run"). Turns an invisible
   loss into a visible one. Does not fix the loss.
2. **Back-pressure instead of drop** for operation-scoped activity, so a burst slows the
   producer rather than discarding history. Needs care: organize's worker pool must not
   deadlock behind a full channel.
3. **Resize / batch** the channel. Simplest, but only moves the threshold — a large enough
   library will always outrun a fixed buffer, so do this *with* (1), never instead of it.

Whatever is chosen, (1) is non-negotiable: a queue that drops data must say how much.

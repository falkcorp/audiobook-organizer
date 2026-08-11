### Fixed

#### Organize was cancelled mid-backup for "no progress", while working correctly

The operations registry does not merely *log* an operation that stops reporting
progress — it **cancels** it after five minutes. Organize's pre-work phases
reported nothing at all, so on a large library the operation was killed while
it was working:

```
06:31:29  library organize starting
06:36:36  registry: strike recorded ... kind=stuck  no progress for 5m8s
```

Three phases ran silent, and each is long on a real library:

- the pre-organize database backup (14 GB on production, 20–25 minutes),
- the full-library fetch, which pages the whole book table 1,000 rows at a time,
- the optional pre-organize metadata fetch, one sequential network call per book.

All three now report. The backup names the phase it is in — snapshotting,
archiving, or verifying — with a running file and byte count, so a backup that
*does* go quiet tells you where it went quiet.

**The stamps are driven by completed work, not by a timer.** This is the point
of the change and it is deliberately the harder option. A goroutine ticking
every fifteen seconds would also have kept the watchdog happy — and would have
kept it happy for a backup that had genuinely wedged, turning a hang detector
into a hang concealer. The backup now reports from inside the archive walk and
the checksum pass, so the counters cannot advance unless bytes actually moved.
A wedged backup still gets cancelled, which is correct.

`TestBackupProgressReporter_IsNotATicker` pins that property: replacing the
reporter with a timer makes it fail with 50 stamps recorded against zero
reported work, while the other tests in the file continue to pass.

### Security

#### core/backup: the offline restore's extraction is bound to the checksum check

`ExtractStore` (the offline restore's archive extractor) checked an archive
against its `.sha256` sidecar and then opened the path again to extract it. An
archive renamed over the path in between was extracted although the check had
matched the original. The extraction now holds what it reads to the digest the
check matched. For a mediacore-backup/1 archive that is the digest of the whole
file as it streams. For a legacy-abo/0 archive it is the scan's digest, checked
before anything is written. On a mismatch it refuses with `ErrChecksumMismatch`
and `backuparchive.ErrChanged`, the staged restore's refusal for the same swap,
and the kernel stages nothing. An archive with no sidecar is unchanged: nothing
was checked, so there is nothing to bind.

### Security

#### core/backup: a legacy archive swapped between scan and extraction is a verification failure

The offline restore's legacy-abo/0 extraction opens the archive again after
the scan and re-hashes what it writes against the scan's digest, so a swap in
between was already refused, but as a bare `backuparchive.ErrChanged` (HTTP
500). When the checksum check vouched for a digest, that refusal now also
carries `ErrChecksumMismatch` (409 `archive_verification_failed`), the same
refusal as a swap before the scan.

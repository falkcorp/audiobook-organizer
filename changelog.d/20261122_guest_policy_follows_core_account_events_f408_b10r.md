### Fixed

- Under core identity the Cloudflare Access guest policy follows core's account events again. Books registers one lifecycle hook with core and runs HEAD's own sync and revoke code from it: accepting a mailed invitation adds the address, and deactivating a guest or changing their address (their own or an admin's) removes the old entry. The keep rules are unchanged: the owner's address, the Cloudflare Access allow-list and another active verified holder of the address keep the entry, and an unverified holder counts for nothing. A Cloudflare failure is logged and never fails the account change. If core hands books no lifecycle handle the boot is refused, never skipped.
- deleting or merging away a guest now also removes their Cloudflare entry (HEAD kept it), under the same keep rules.
- The identity cutover (M3) now carries HEAD's record that the sync added a guest's address to Cloudflare into books' own `guestpolicy:` rows. Without it a guest added by the sync before the cutover lost that fact, and their entry could never be removed afterwards.

### Added

- Books keeps its own guest-policy bookkeeping under core identity (`guestpolicy:` rows and one outcome row per core invite id, `guestpolicyout:`), since core's invites and accounts carry neither field. The admin screens that show the outcome are wired separately.

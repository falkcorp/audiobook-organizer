### Fixed

- Under core identity books now registers ONE account lifecycle hook with core, and both guest concerns follow core's account events through it. The Cloudflare Access guest policy: accepting a mailed invitation adds the address, and deactivating a guest or changing their address (their own or an admin's) removes the old entry, by HEAD's own sync and revoke code and with HEAD's keep rules (the owner's address, the Cloudflare Access allow-list and another active verified holder of the address keep the entry; an unverified holder counts for nothing). The guest's phone-app key: accepting a mailed invitation offers the one-time key, and deactivating the guest revokes it. Each concern runs under its own recover, so a failure in one never skips the other, and neither fails the account change. If core hands books no lifecycle handle the boot is refused, never skipped.
- deleting or merging away a guest now also removes their Cloudflare entry (HEAD kept it), under the same keep rules, and revokes their phone-app key (HEAD kept that too). Both only take access away.
- Core refuses to accept an invitation whose creator has lost the admin role (the link answers 401 `link_invalid`). HEAD accepted it and skipped the grant. The guest never gets an account that could hold a Cloudflare entry or a phone-app key from such an invitation.
- The identity cutover (M3) now carries HEAD's record that the sync added a guest's address to Cloudflare into books' own `guestpolicy:` rows. Without it a guest added by the sync before the cutover lost that fact, and their entry could never be removed afterwards.

### Added

- Books keeps its own guest-policy bookkeeping under core identity (`guestpolicy:` rows and one outcome row per core invite id, `guestpolicyout:`), since core's invites and accounts carry neither field. The admin screens that show the outcome are wired separately.

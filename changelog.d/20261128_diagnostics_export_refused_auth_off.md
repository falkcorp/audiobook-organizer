### Changed

- Starting a diagnostics export now answers 409 right away when core authentication is disabled, with a message that says so (a diagnostics export needs a signed-in account, and core authentication is disabled). Before, the export was queued and then always failed, with a message that told you to sign in and start it again, which cannot be done in that mode. Nothing is widened: an export still needs an account to narrow it to. The failure message for an export whose request carried no signed-in account no longer says to sign in again when the account is the problem.

### Changed

- Groundwork for moving sign-in to the shared identity service (nothing changes for users yet). Password sign-in and token refresh on the Audiobookshelf-compatible surface now check the password, the account's status and its credential changes against that service when the server is built to use it, instead of the app's own user records, so phone apps keep signing in and refreshing once the accounts move there, and a password change or an account being switched off there ends a session at once. All of it stays dormant until the switch-over.

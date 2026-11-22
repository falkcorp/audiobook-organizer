### Changed

- After the switch to the new sign-in system, the 19 admin pages and actions that the old release opened only to the role named "admin" will be opened by permission instead. Any role given the matching permission will reach them, and so will an API key holding it, except on the guest access token pages and the three debug edit actions, which no API key can use. The owner approved this on 2026-11-22. Only someone who already holds a permission can hand it out, so every grant starts with an administrator, and nothing that existed before the switch gains access. The specs record this, and new tests check both halves: the permission opens the route, and a non-admin cannot grant it.

### Security

- Under the new sign-in system, the old API-key page now refuses itself when an API key tries to create a key for another user, with the message "API keys cannot create keys for other users; sign in to do that". Before, that refusal came only from the route's shared guard. A key creating a key for itself, and an admin who is signed in creating a key for someone else, work as before. The new system's own key routes also refuse a key that tries to create, disable or revoke another user's key.

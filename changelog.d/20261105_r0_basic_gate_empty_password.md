### Security

#### Basic authentication refuses an empty saved password

Basic authentication with an empty saved password is refused. If Basic authentication is on without a saved password, save one before upgrading; if you are locked out, set `BASIC_AUTH_PASSWORD` and restart.

With Basic authentication on and no password saved, the gate used to compare
two empty strings and let in anyone who sent the username with an empty
password. Now every route except health, static assets and the
Audiobookshelf routes that check their own token answers 401 while there is
no password, and `/api/v1/health` lists `basic_gate_no_password` under
`degraded` so the cause is visible without signing in. The startup log names
the setting (`basic_auth_enabled`, `basic_auth_password`).

When the saved password is empty, `BASIC_AUTH_PASSWORD` is used instead, in
memory only: it is never saved, the log says so once at warn without the
value, and a value that is empty or only spaces counts as unset. A saved
password still wins over the variable. Saving a password in Settings clears
the health token without a restart.

A saved password made only of spaces now counts as no password too, so it is
refused the same way; `BASIC_AUTH_PASSWORD` recovers such an install. Any
other saved password is compared exactly as saved. The gate also reads its
settings once per request, together, so a request can no longer race a
Settings save.

### Fixed

#### The old sign-out route no longer claims a sign-out it did not do

Under core identity, `POST /api/v1/auth/logout` used to answer "logged out" and clear the cookie, but it revoked a session from the old session rows, which the identity cutover empties. Core's session stayed valid. The route now answers 410 Gone and points at `POST /api/core/v1/auth/logout`, which ends the session. The web app already calls core's route. The old route changes nothing and clears no cookie.

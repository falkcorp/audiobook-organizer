- [ ] **ADMIN-COMPROMISED-ACCOUNT-RESET** Admin "compromised account" reset
      should invalidate the old password. Owner decision. Today the admin
      reset link (`POST /users/:id/reset-password`, `handleResetPassword`)
      signs the user out of every ABS client (credential-epoch bump, #4081) and
      issues a one-time login link, but the old password keeps working until
      the user picks a new one, so an attacker who has it can still sign in.
      Options: (a) a separate "compromised" action that also replaces the
      password hash with an unusable value in the same `SetUserPassword`
      write, (b) make the existing reset do that, or (c) keep the current
      behaviour and label it as a convenience reset in the UI. Web sessions
      have the same gap (see WEB-SESSIONS-SURVIVE-PASSWORD-CHANGE).

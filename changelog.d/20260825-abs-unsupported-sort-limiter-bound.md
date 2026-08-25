### Fixed

- **The new unsupported-sort warning could be made to consume memory, and could
  be silenced permanently.** The warning remembered every distinct sort value a
  client had sent so it would only report each one once. It remembered the value
  in full — a sort parameter can be up to a megabyte — and stopped remembering
  new ones after 64, which meant two things: a client sending long junk values
  could make the server hold tens of megabytes for as long as it ran, and a
  client sending 64 different junk values permanently used up the budget, after
  which the server went back to saying nothing about genuinely unsupported
  sorts. The warning now remembers only *when* it last spoke, not *what* was
  asked for, and reports at most once a minute. It cannot be flooded, and it
  cannot be switched off by a client.

  The endpoint requires authentication, so this needed a valid client or a
  leaked token; an earlier note describing it as unauthenticated was wrong.

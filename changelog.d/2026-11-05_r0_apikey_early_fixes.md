### Security

- A revoked API key can no longer be re-activated; a limited API key can no
  longer create an unlimited one; a revocation can no longer be undone by a
  request that was in flight.
- An API key that creates another key must now name that key's scopes, each
  one a permission the creating key holds. A key created without scopes would
  get every permission of its owner's roles, which a limited key must never
  hand out. Creating a key from a signed-in session is unchanged.

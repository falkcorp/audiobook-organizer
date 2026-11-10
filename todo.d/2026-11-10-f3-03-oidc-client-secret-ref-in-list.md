- [ ] **F3-OIDC-SECRET** Let the config engine bind a `secrets.Ref` inside a list
      entry, then declare each OIDC provider's `client_secret` — 05-config
      §2.2 and §1.12.3 (`oauth_*_client_secret`) make it a secret, but
      `core/config`'s decoder refuses a `Ref` inside a list element, and
      `core.auth.oidc.providers` is a list. F3-03 declared only
      `core.http.basic_gate.password`. Done means: the decoder binds each
      entry's `Ref` to the spec's `core.auth.oidc.<id>.client_secret`, built
      from the entry's id, `TestCoreSections_SecretKeys` lists it, and
      `TestCoreSections_NoLaterRowsKeys` stops refusing it
      (seams.d/F3-03.md, "Spec readings").
      - Owner: 05-config's owner (F3); before F3-07 wires OIDC sign-in.

- [ ] **F7-09-MD-CRED-OPERATOR** After F7-09, a metadata source's credential set by
      environment variable (`ABO_INTEGRATIONS_METADATA_<ID>_CREDENTIALS_<K>`) or by the
      operator file (`integrations.metadata.<id>.credentials.<k>`) is unsupported: the
      credential is a stored secret, set in Settings, Metadata tab (the source's API key
      field) or through `PUT /api/v1/config` (`settings.manage`). A host that still sets
      one is refused at deploy preflight (`scripts/deploy-preflight.sh`, the
      `deploy/retired-env-names.txt` rows) and at boot (`books.refuseRetiredOperatorSettings`),
      naming the key and never the value. Add the pointer to the operator-facing upgrade
      notes and the Metadata settings help text, and confirm on the prod-copy rehearsal that
      `f709_retired_operator_paths_unset` passes (it covers this family and the old
      `integrations.ai.openai.base_url`).

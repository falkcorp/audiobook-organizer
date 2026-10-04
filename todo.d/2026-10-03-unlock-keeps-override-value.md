- [ ] **EDIT-UNLOCK-KEEPS-OVERRIDE** `unlock_overrides` (and an override with
      `locked:false`) on the book edit endpoint only clears a field's
      OverrideLocked flag; the stored OverrideValue stays, and
      MetadataFieldState.HasUserOverride / database.LockedUserFields treat any
      stored override value as a user override. So an "unlocked" field is
      still locked for every guard (metadata apply, StripLockedFields).
      Evidence (PR #3698 review probe TestP15_UnlockIsNotAnUnlockForGuards):
      PUT {"unlock_overrides": ["publisher"]} after a locked publisher edit
      leaves OverrideLocked=false, OverrideValue="NP" and
      LockedUserFields[publisher]=true. Decide what an unlock means (drop the
      value, or make the guards read the flag), fix it in one place, and
      re-check every caller of HasUserOverride. The edit endpoint already
      stores no value for a changed field sent with locked:false.

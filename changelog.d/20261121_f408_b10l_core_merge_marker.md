### Added

- Core identity records which account an admin user merge folded an account into (`auth.User.MergedInto`, `MergedAt`; `merged_into_user_id` and `merged_at` on the user JSON). `auth.Service.SetStatus` now refuses to enable such an account (`auth.ErrMergedAway`, HTTP 409 `merged_away`), as HEAD's reactivate route did, so a merged-away account cannot be turned back on to share sign-ins and data with the account that absorbed it. The HEAD import carries the marker over for rows merged before the cutover.

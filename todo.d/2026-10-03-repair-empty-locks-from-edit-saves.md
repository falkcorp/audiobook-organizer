- [ ] **EDIT-BLANK-LOCKS** Repair the field locks the book edit endpoint wrote
      before PR #3698. Every BookDetail save sent description, publisher,
      language, narrator, author and series as "" or as their current value,
      and the endpoint locked each one: at "" for an empty field, at the old
      value for every other field present. Those locks stop metadata fetches
      from filling or upgrading the field. Count them on prod first (locked
      overrides whose value is "" or equals the column, with no matching
      user_edit history row that changed the value), then build a dry-run
      repair op that unlocks them; owner approves before the live run. Done
      = count reported, op merged, dry run reviewed, live run applied.

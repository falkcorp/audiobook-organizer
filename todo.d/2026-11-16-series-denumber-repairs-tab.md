- [ ] **SERIES-DENUMBER-REPAIRS-TAB** Surface the series de-numbering plans that are not
      applied unattended (the low tier, the plans demoted by the name grammar, the books held
      back as position_uncorroborated, series_locked, base_under_another_author or for a
      position conflict) in the Repairs tab, so an owner can approve them one at a
      time. Design: a fixer whose trial lists one numbered series with its books, the target base
      and the position each book would take, and whose apply runs the same per-book move as the
      operation (`internal/seriesdenumber.Decide` for the position check, `sdn.MoveRows` for the
      undo rows, `series_create` and `series_row_delete` for the series rows) under the fixer's
      own operation id. The decision logic is already a pure package, so the fixer needs only the
      store reads and the Repairs registration. It was not wired in the same change because a
      fixer also needs its own trial/approve/apply states and an undo path through the Repairs
      journal, which is more than a contained change.

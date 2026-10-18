- [ ] **01-P81J-HOLD-FAILED-FIRST-RUN** Decide whether a failed FIRST-run review apply
      should be held the way a failed re-run is. Today a first run whose apply handler
      returns an error is released back to the status it was claimed from, so the item
      is decidable again (`applyUnderClaim` in
      `internal/server/handlers/review/handler.go`); a failed re-run of an interrupted
      apply stays `applying` as interrupted until approved again or abandoned. That
      assumes a failed first run changed nothing, which is not guaranteed:
      `ApplyVersionGroup` makes two writes, and a `duplicate-of` merge can stop after
      a partial merge, so the item can come back as pending or approved over a
      half-changed library, where a reject or a different action can then be recorded
      on top of it. Options: (a) hold every failed first run as interrupted, the same
      as a re-run. Safe, but a failure that wrote nothing (the common case: a refused
      precondition, the iTunes guard) then needs an operator to approve again or
      abandon, and the bulk report would list far more held items. (b) Have each apply
      handler report whether it wrote anything (a typed "nothing written" error, or a
      result value), and hold only the first runs that did. Precise, but every handler
      has to get that answer right, and a handler that wrongly says "nothing written"
      is exactly today's hole. (c) Keep today's behaviour and rely on the reason text
      and the idempotent re-run. Cheapest; leaves a partial apply decidable as if it
      had not happened. Owner decision; no behaviour was changed by 01-P81j for this.

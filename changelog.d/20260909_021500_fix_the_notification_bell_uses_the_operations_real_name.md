### Fixed

- **The notification bell now calls an operation by the same name the Activity
  page does.** The bell built each row's label from the operation's internal
  type, so anything whose real name is not mechanically derivable from that type
  was announced under a mangled one — a run the Activity page called "AI
  Filename Parsing" appeared in the bell as "Ai Parse". Both surfaces now use the
  name the server supplies.

  This survived a previous fix aimed squarely at it because the label function
  never fails: it title-cases whatever it is handed, so a wrong name looks like a
  real name and nothing — no test, no type error — could point at it. The test
  covering this had been written to expect the mangled form, which is how it read
  as passing.

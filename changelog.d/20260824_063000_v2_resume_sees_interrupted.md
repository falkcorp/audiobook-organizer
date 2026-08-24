### Fixed

- An operation interrupted by a restart is now actually resumed. The startup
  resume sweep took its candidates from the "active" index, which drops a row the
  moment its status stops being queued or running — so every operation a graceful
  shutdown left `interrupted_quiesced` became invisible to it and never came back,
  no matter what its resume policy said. A library scan stranded this way sat
  untouched for hours across a deploy. The sweep now scans for interrupted rows
  directly, and only the most recent interrupted run of each operation is
  resumed — a live queued or running run always wins — so a month of accumulated
  interruptions cannot all restart at once.

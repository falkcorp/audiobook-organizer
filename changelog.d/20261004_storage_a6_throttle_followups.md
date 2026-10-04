### Changed

#### Progress log throttle now also covers messages that embed a basename

The first throttle compared digit-normalised message shapes, but `library.scan`, the folder discovery walk and the reconcile apply append a basename, path or book id to every progress message, so each line was a new shape and was still logged. Shape-change lines are now capped at four per 30-second window; past that the stream falls back to the 30-second rule. The message shown in the UI is unchanged. A shape change also writes the old phase's last suppressed line first, so a phase's final tally is kept. The final progress line is written before the "operation finished" line, and the subprocess child now drains its reporter before exiting. `progressShape` treats a sign and a decimal part as part of the number.

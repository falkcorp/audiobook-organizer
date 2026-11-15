### Changed

- core: the F8-03b guard fails outright when the marker is set to false (it has two states, true and deleted; the round 9 check tied to the scanner's names never fired), pins the run-time refusal's probe to `testing.Testing`, and `TestRedesignRefusal_RealBinary` builds and runs a production binary to prove `diagnostics.New` and `Exporter.Build` refuse in it. No build path may set the linker flag that turns the refusal off.
- docs: F8-03b is a row of the plan (inventory, implementation plan, task summary, README); `check_waves.py` accepts a lowercase letter after the number of a row id.

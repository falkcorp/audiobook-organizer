### Added

- Internal: core's in-process test harness and the route-permission gate. `coretest.New` boots core in process with no listener and hands tests a client that sends requests to the App's handler, plus the G1 "disabled module is inert" assertion. `archtest.RoutesHavePermissions` fails a build whose router has a route without an admissible permission, naming each one. The shared test log capture moved to the leaf package `coretest/logcapture` so core's own kernel and app tests can still use it.

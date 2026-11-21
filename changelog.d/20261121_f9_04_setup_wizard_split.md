### Changed

- The first-run setup wizard is now built from a setup frame (dialog, steps, Back and Next) and the library module's setup steps, and the "does this install still need setup" check lives with the library module. The wizard looks and behaves the same, and a new install is still sent to it exactly when it has no library folder and setup was never marked complete.

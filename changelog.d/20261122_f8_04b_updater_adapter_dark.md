### Added

- core/kernel: the updater adapter for the F8-04b flip (`core/kernel/updater.go`), dark. It holds constructors only: the `updater.Options` and `Deps` builder (HTTP and Notify may be nil, as at HEAD), the topic spec, the `c/updater/` storage owner spec, the `core.updater` health source, the task starter and the `updaterhttp.Register` mount. Nothing calls them at boot, so no topic, owner, probe, route or task is registered and the running app behaves as before.

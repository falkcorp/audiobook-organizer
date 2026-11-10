### Added

#### `core/config` declaration surface and every core config section declared (dark)

New package `core/config` holds only the declaration half of the config API: `Path` with its grammar check, `Reload`, `Protection`, `Duration` (a Go duration string in JSON), `Validator`, `FieldError`, `Section[T]`, `Define` and the options `Permissions`, `Docs`, `ProtectionWaivers`, `EnvBinding` and `FailClosed`. The loader, the store and `Register` follow with F3-01.

`core/kernel/core_sections.go` lists every core section with the key table its owner spec states, 208 keys in 32 sections, whether or not that subsystem is wired yet. Schedule keys, the task windows, the two secret keys and the fail-closed declarations are added by their own later rows. Nothing reads these keys and no route, schedule or stored row changes, so behaviour is unchanged.

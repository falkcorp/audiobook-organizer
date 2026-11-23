### Added

- A new architecture test, `TestDescriptorCallers_*` in `internal/arch`, pins which audiobook-organizer packages may build or register an integration descriptor: only the metadata and whisper adapter packages may reference the kind constructors, and no package may reference `integrations.NewDescriptor` or the core adapters' `Descriptor()` functions until the integrations flip lists its one registering package. It is the audiobook-organizer twin of the core archtest and changes no running code.

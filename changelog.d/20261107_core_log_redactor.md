### Added

- Internal: one credential redactor for logs and traces in the core module. When tracing is on, every exported span now has API keys, tokens, passwords and invite links removed from its name, attributes, events and status before it leaves the server, and the server refuses to start tracing without the redactor.

### Fixed

- Core: in a settings section that holds a sensitive setting, a message from the section's own validation can no longer leak that setting's value through a plain setting beside it. Every such message is now replaced by a fixed one, and only the setting it names is kept. A section with no sensitive setting keeps its validation messages.

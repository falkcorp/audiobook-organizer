### Fixed

- Core: in a settings section that holds a sensitive setting, a message from the section's own validation can no longer leak that setting's value through a plain setting beside it. Every such message is now replaced by a fixed one, and only the setting it names is kept. In a list-of-entries section, that setting is kept only when it belongs to an entry that exists, so a made-up entry name cannot carry the value either. A section with no sensitive setting keeps its validation messages.
- Core: saving a setting whose stored value had been set aside as invalid, with that same value, no longer reports success while nothing changes. The value is checked again: it is saved and the setting is no longer set aside, or the save is refused with an error.

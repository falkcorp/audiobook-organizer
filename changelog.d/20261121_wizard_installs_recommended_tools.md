### Fixed

- The first-run wizard's "Install recommended tools" choice now does what it says. Complete Setup used to ignore it, so nothing was installed. After the setup is saved and the wizard closes, it installs fpcalc and then Ollama in the background and reports the result in a toast. A tool that is already installed counts as installed, and a failed install names the tool and points to Settings, Tools. "Let me choose" and "Skip" install nothing.

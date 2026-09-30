### Changed

- Woodpecker CI runs each commit once: on `push` to `main`, on pull requests, and on manual runs. A branch push with an open PR used to start a second, identical pipeline, which doubled the load on U1 and made web tests time out.

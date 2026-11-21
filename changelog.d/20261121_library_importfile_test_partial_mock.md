### Fixed

- A Library import test that had stopped passing no longer breaks when another part of the web app starts using a new server call: the test now keeps the real set of calls and replaces only the ones it drives.

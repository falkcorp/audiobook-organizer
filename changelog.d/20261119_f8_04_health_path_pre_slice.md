### Changed

- The local test scripts, the developer guide, the deploy skill's post-deploy check and the embedded-binary smoke workflow now probe `/health` instead of `/api/health` or `/api/v1/health`. Nothing about the server changes: all three paths still answer. This moves the callers ahead of the later removal of the two older paths.

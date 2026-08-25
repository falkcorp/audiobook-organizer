### Fixed

#### Server bootstrap skill documents the usable bearer-key response

The server-bootstrap instructions now wait for the fresh token file after a
service restart, request the pseudo-terminal required by production sudo, and
extract the issued bearer key from the API's standard `data` response envelope.

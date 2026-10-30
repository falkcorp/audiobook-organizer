### Fixed

- `GET /system/logs?operation_id=` returned nothing for every caller: it handed the request to a handler that read the operation id from a route parameter this route does not have. The operation id is now passed as an argument, and the same visibility check as the operation's own log route applies.

### Fixed

- The activity log now reads an operation's `op_id` and `component` from the exact attribute key, so a message or file name containing text like `opID=other` can no longer attach the activity row to the wrong operation or component.

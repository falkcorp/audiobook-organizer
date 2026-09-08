### Fixed

- Long-running jobs could show two different names on the same screen — a
  readable one in the notifications panel and a raw internal identifier
  (`activity.sql-migration`) in the operations list on the Activity page. Both
  now use the same label, and a job with no registered name is de-slugged
  instead of being printed as-is.

### Changed

- The iTunes clone into library operation now removes the clone files it made (the cleanup after a failed mixed apply, and a rollback's file and emptied-folder removal) through the run's writer as a `files` write, instead of plain `os.Remove` calls beside it. A run that has declared `files` now fences and records those removals, where before they could happen after the last fenced write.

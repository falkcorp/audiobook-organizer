### Fixed

- Authors and series review: a merge that finished while the list was being reloaded (for example one you came back to after leaving the page) could leave the merged-away rows showing, because the reload returned a list from before the merge. The merge is now applied to whatever the reload returns.

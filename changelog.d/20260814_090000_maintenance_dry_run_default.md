### Fixed

- Maintenance jobs no longer run for real when you asked for a preview. Most
  maintenance jobs (18 of 34) are meant to preview their changes by default, and
  the settings screen showed them that way — but the server ignored that and
  applied the changes for real whenever the preview setting wasn't spelled out in
  the request. The two now agree, and a job you explicitly set to apply still
  applies.
- A preview that was interrupted by a server restart no longer comes back as a
  real change. The server kept no record of whether a running job was a preview,
  so when it restarted and picked the job back up it treated every one of them as
  a real change — including the job that deletes empty folders from disk. Your
  choice is now saved with the job and restored on restart.

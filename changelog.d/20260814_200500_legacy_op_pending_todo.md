### Changed

- Filed the stuck-pending legacy op rows defect: maintenance jobs' legacy
  operation rows never receive terminal status, so the ops UI shows finished
  work as running for hours (misled the operator twice today).

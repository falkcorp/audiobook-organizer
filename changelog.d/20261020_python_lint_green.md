### Changed

#### Python scripts and tests now pass the CI Python lint (`ruff format --check` and `ruff check` over `scripts` and `tests`, ruff 0.17.0) with no config changes, ignores or `noqa` additions; 55 files reformatted and 100 lint findings fixed, including narrowing three bare `except:` clauses, renaming `finish_credential_migration.Refused` to `RefusedError`, and moving the FastAPI upload parameters in the Whisper workers to `Annotated[..., File()]`

### Changed

#### Python scripts and tests pass the CI Python lint

`ruff format --check` and `ruff check` (ruff 0.17.0) over `scripts` and `tests` now exit 0 with no config changes, new ignores or excludes. 55 files were reformatted and 100 lint findings fixed. Notable behaviour-adjacent changes: two bare `except:` clauses were narrowed (and one dead one in `scripts/rebase-branches.py` replaced by a `None` check), `finish_credential_migration.Refused` is now `RefusedError`, and the FastAPI upload parameters in the Whisper workers use `Annotated[..., File()]`. The final `git stash pop` in `scripts/rebase-branches.py` now runs even if the branch restore is interrupted.

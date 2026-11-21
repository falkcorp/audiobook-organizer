### Changed

- `make deploy-release` now carries `deploy/local.conf` like `make deploy`: it runs `deploy-env-check` first, uploads the file (when it exists; `DEPLOY_LOCAL_CONF` names another) to the rollback helpers' `stage_local_conf` and has `install-staged local-conf` render it, so a release deploy applies env renames. The environment the replaced binary ran under is still kept for `make rollback`. `deploy-env-check` takes its file from the same variable, default unchanged.

### Added

#### Core backup adapter and books restore stager (F8-04a G2, not yet live)

The kernel can now build core's backup service over the journal handle the identity boot already holds, the audit store, the secrets keyring and the swapper, with the restart request going through the lifecycle's stop order. Nothing mounts it or registers its tasks yet, so the running app is unchanged. Staging a restore without an interactive owner proof is still refused, and a restore started by an API key never carries one. The audit sequence gained a `Cut` that ends its current id lease, so a backup's recorded audit boundary really bounds every record written after it. Core backup also gained a before-stage hook that runs after the swapper's own refusals and before the intent is written, and books has a restore stager over it that keeps the vector-index clean-stop ordering; it is not installed in place of the current one.

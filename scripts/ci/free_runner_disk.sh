#!/usr/bin/env bash
# file: scripts/ci/free_runner_disk.sh
# version: 1.0.0
# guid: 8c3f1a6e-2d94-4b57-a0e8-6f1b9d4c2e73
# last-edited: 2026-11-04
#
# Frees disk on a GitHub-hosted ubuntu runner before a job restores the Go
# cache. The image leaves about 14 GB free. The shared Go cache is about
# 2.6 GB compressed (go-build + pkg/mod), and a -race build plus one linked
# test binary per package can fill what is left.
#   2026-10-31: #4330/#4331 race shards hit "no space left on device" in the
#               linker.
#   2026-11-04: #4361 Coverage Floor and #4362 Errcheck Ratchet died
#               mid-restore of that cache. The runner could not even write
#               its own diag log, so the job shows no output at all.
# The 10-31 fix covered only the race test shards. Every job that restores
# the Go cache now calls this one script, so no job is left out again.
# Removing the preinstalled .NET, Android, GHC and CodeQL toolchains (no job
# here uses them) frees about 25 GB.
set -uo pipefail
df -h /
sudo rm -rf /usr/share/dotnet /usr/local/lib/android /opt/ghc /opt/hostedtoolcache/CodeQL
df -h /

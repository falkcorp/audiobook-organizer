#!/bin/bash
# file: scripts/ci/cache_env.sh
# version: 1.3.0
# guid: 6b0e7d3a-92c4-4f18-a5d7-1e8c3f40b9a2
# last-edited: 2026-10-17
# Source this from a Woodpecker step (`. scripts/ci/cache_env.sh`) as the
# step's first command. It does two things: it unsets the provider keys and
# live-service URLs that would turn on a live external call (below), and it
# points Go and npm at the host-wide CI cache of whichever heavy agent runs
# the step.
#
# The workflows that run on any `heavy=true` agent (U1 or llm1), or any
# `taglib=true` agent (U1 or u0-local), cannot name a cache path in
# `environment:`: it is a literal, and the hosts keep the cache in different
# places. The local backend gives every pipeline a fresh
# HOME, so Go's default caches would be re-created per run, which is why the
# workflows set the caches explicitly.
#
#   U1    Linux amd64   /tank/ci/cache          (the agent's own ZFS dataset)
#   llm1  macOS arm64   /Users/jdfalk/ci/cache  (there is no /tank)
#   u0-local Linux amd64 /srv/ci/cache          (its own ZFS dataset on prod)
#
# The first root that exists wins. It exports CI_CACHE_ROOT, GOMODCACHE,
# GOCACHE and npm_config_cache. If no root exists it returns 1 instead of
# letting Go fall back to a per-run HOME cache that the agent cannot tear down.
# Callers that must stop on that use `. scripts/ci/cache_env.sh || exit 1`.

# CI never calls paid providers or live services. internal/ai's integration
# tests run when OPENAI_API_KEY is set (and the openai-go SDK reads it from env),
# ACOUSTID_API_KEY reaches config through viper.BindEnv, AutomaticEnv
# (cmd/root.go) maps the other provider keys when the binary runs, and
# WHISPER_LIVE_URL turns on internal/transcribe's live capability probe. On
# 2026-10-12 the steps on a Mac agent saw OPENAI_API_KEY, so test-rest called
# OpenAI for real and failed on 429 (pipelines 874 and 882). Every step on the
# heavy (local-backend) agents sources this file first, so unsetting here holds
# whatever the host exports. It runs before the cache-root check, so a step
# that continues past a missing cache root (`|| true`) is scrubbed too. U0's
# checks-lint steps run in docker containers with a clean environment and run
# no tests.
unset OPENAI_API_KEY ANTHROPIC_API_KEY ACOUSTID_API_KEY GOOGLE_BOOKS_API_KEY HARDCOVER_API_TOKEN WHISPER_LIVE_URL


CI_CACHE_ROOT=
for _ci_cache_candidate in /tank/ci/cache /Users/jdfalk/ci/cache /srv/ci/cache; do
  if [ -d "$_ci_cache_candidate" ]; then
    CI_CACHE_ROOT=$_ci_cache_candidate
    break
  fi
done
unset _ci_cache_candidate

if [ -z "$CI_CACHE_ROOT" ]; then
  echo "cache_env.sh: no CI cache root found (looked for /tank/ci/cache, /Users/jdfalk/ci/cache and /srv/ci/cache)" >&2
  return 1 2>/dev/null || exit 1
fi

mkdir -p "$CI_CACHE_ROOT/gomod" "$CI_CACHE_ROOT/go-build" "$CI_CACHE_ROOT/npm" || {
  echo "cache_env.sh: cannot create cache directories under $CI_CACHE_ROOT" >&2
  return 1 2>/dev/null || exit 1
}
export CI_CACHE_ROOT
export GOMODCACHE="$CI_CACHE_ROOT/gomod"
export GOCACHE="$CI_CACHE_ROOT/go-build"
export npm_config_cache="$CI_CACHE_ROOT/npm"
echo "CI cache root: $CI_CACHE_ROOT"

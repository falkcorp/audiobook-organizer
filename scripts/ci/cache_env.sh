#!/bin/bash
# file: scripts/ci/cache_env.sh
# version: 1.1.0
# guid: 6b0e7d3a-92c4-4f18-a5d7-1e8c3f40b9a2
# last-edited: 2026-10-12
# Source this from a Woodpecker step (`. scripts/ci/cache_env.sh`) as the
# step's first command. It does two things: it unsets the paid-provider API
# keys (below), and it points Go
# and npm at the host-wide CI cache of whichever heavy agent runs the step.
#
# The workflows that run on any `heavy=true` agent (U1 or llm1) cannot name a
# cache path in `environment:`: it is a literal, and the two hosts keep the
# cache in different places. The local backend gives every pipeline a fresh
# HOME, so Go's default caches would be re-created per run, which is why the
# workflows set the caches explicitly.
#
#   U1    Linux amd64   /tank/ci/cache          (the agent's own ZFS dataset)
#   llm1  macOS arm64   /Users/jdfalk/ci/cache  (there is no /tank)
#
# The first root that exists wins. It exports CI_CACHE_ROOT, GOMODCACHE,
# GOCACHE and npm_config_cache. If no root exists it returns 1 instead of
# letting Go fall back to a per-run HOME cache that the agent cannot tear down.
# Callers that must stop on that use `. scripts/ci/cache_env.sh || exit 1`.

# CI never calls paid providers. A test that finds OPENAI_API_KEY set runs the
# live OpenAI integration tests (internal/ai/openai_parser_test.go), and viper's
# AutomaticEnv lets the provider keys reach config. On 2026-10-12 the steps on
# a Mac agent saw OPENAI_API_KEY, so test-rest called OpenAI for real and
# failed on 429 (pipelines 874 and 882). Unsetting here
# holds whatever the host exports, because every Go step sources this file
# first; it runs before the cache-root check so a step that continues past a
# missing cache root (`|| true`) is scrubbed too.
unset OPENAI_API_KEY ANTHROPIC_API_KEY ACOUSTID_API_KEY GOOGLE_BOOKS_API_KEY HARDCOVER_API_KEY

CI_CACHE_ROOT=
for _ci_cache_candidate in /tank/ci/cache /Users/jdfalk/ci/cache; do
  if [ -d "$_ci_cache_candidate" ]; then
    CI_CACHE_ROOT=$_ci_cache_candidate
    break
  fi
done
unset _ci_cache_candidate

if [ -z "$CI_CACHE_ROOT" ]; then
  echo "cache_env.sh: no CI cache root found (looked for /tank/ci/cache and /Users/jdfalk/ci/cache)" >&2
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

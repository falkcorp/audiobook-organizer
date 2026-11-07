#!/usr/bin/env bash
# file: scripts/ci/set_bot_git_identity.sh
# version: 1.0.0
# guid: 4e9b7c21-5d38-4a6f-8c14-b2a07e3d9f56
# last-edited: 2026-11-06
#
# Usage: GH_TOKEN=<app token> set_bot_git_identity.sh <app-slug>
#
# Set the repo-local git author/committer to the GitHub App's bot account, so a
# bot commit is attributed (and its avatar linked) to the app rather than to
# github-actions[bot]:
#
#   user.name  = <app-slug>[bot]
#   user.email = <bot-user-id>+<app-slug>[bot]@users.noreply.github.com
#
# <app-slug> is the `app-slug` output of actions/create-github-app-token. The
# bot's numeric user id is looked up at runtime, never hard-coded, so renaming
# or recreating the app needs no edit here. The commit identity is cosmetic;
# which actor a ruleset bypass applies to is decided by the token that pushes.
set -euo pipefail

slug=${1:?usage: set_bot_git_identity.sh <app-slug>}

# GitHub App slugs are lowercase letters, digits and hyphens. Anything else means
# the argument is wrong (an empty or unresolved expression, for instance).
if ! [[ $slug =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
  echo "::error::unexpected GitHub App slug '${slug}'" >&2
  exit 1
fi

# %5B / %5D are the brackets of "<slug>[bot]".
bot_id=$(gh api "users/${slug}%5Bbot%5D" --jq .id)
if ! [[ $bot_id =~ ^[0-9]+$ ]]; then
  echo "::error::could not resolve the bot user id for '${slug}[bot]' (got '${bot_id}')" >&2
  exit 1
fi

git config user.name "${slug}[bot]"
git config user.email "${bot_id}+${slug}[bot]@users.noreply.github.com"
echo "git identity: ${slug}[bot] (user id ${bot_id})"

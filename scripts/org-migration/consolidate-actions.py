#!/usr/bin/env python3
# file: scripts/org-migration/consolidate-actions.py
# version: 1.0.2
# guid: 7444eba2-0f96-4642-806c-e56f0a50d0e9
# last-edited: 2026-10-20

"""Phase 1: Write consolidated action.yml files for 5 gha-release-* repos.

Each consolidated action inlines the full base implementation so that
callers no longer depend on a cross-repo uses: chain.
"""

import os
import subprocess

HOME = os.path.expanduser("~")
BASE = f"{HOME}/repos/github.com/jdfalk"

# Local dirs (wrappers) that point to falkcorp/gha-release-* remotes
WRAPPER_DIRS = {
    "go": f"{BASE}/gha-release-go",
    "python": f"{BASE}/gha-release-python",
    "rust": f"{BASE}/gha-release-rust",
    "docker": f"{BASE}/gha-release-docker",
    "frontend": f"{BASE}/gha-release-frontend",
}


def run(cmd, cwd=None, check=True):
    print(f"  $ {' '.join(cmd) if isinstance(cmd, list) else cmd}")
    result = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    if check and result.returncode != 0:
        print(f"  STDERR: {result.stderr}")
        raise RuntimeError(f"Command failed: {cmd}")
    return result.stdout.strip()


# ─── GO ───────────────────────────────────────────────────────────────────────
GO_ACTION = """\
# file: action.yml
# version: 2.0.0

name: 'Go Release Build'
description: 'Build and release Go binaries via GoReleaser'
author: 'falkcorp'

branding:
  icon: 'box'
  color: 'blue'

inputs:
  # --- orchestration inputs (from wrapper) ---
  go-matrix:
    description: 'JSON encoded Go build matrix (informational; steps use module-path)'
    required: false
    default: '{}'
  protobuf-artifacts:
    description: 'Whether protobuf artifacts are available from an earlier job step'
    required: false
    default: 'false'
  release-version:
    description: 'Release version to embed in binaries (overrides github.ref_name)'
    required: false
    default: ''
  system-packages:
    description: 'Space-separated list of apt packages to install before build'
    required: false
    default: ''
  pre-build-script:
    description: 'Shell script to run before goreleaser'
    required: false
    default: ''
  go-experiment:
    description: 'Value for GOEXPERIMENT environment variable'
    required: false
    default: ''
  github-token:
    description: >
      Token used for git push of release tags. Must have workflows:write when
      the tag includes commits that modify .github/workflows/ files (typically
      a GitHub App installation token). Falls back to the default GITHUB_TOKEN
      for runs that do not touch workflow files.
    required: false
    default: ${{ github.token }}

  # --- implementation inputs (from base) ---
  module-path:
    description: 'Path to Go module (relative to repository root)'
    required: false
    default: '.'
  go-version:
    description: 'Minimum Go version to use (auto-upgraded if go.mod requires higher)'
    required: false
    default: '1.24'
  is-sdk:
    description: 'Whether this is an SDK module (enables SDK-specific tagging)'
    required: false
    default: 'false'
  sdk-language:
    description: 'SDK language (go, python, typescript, etc.)'
    required: false
    default: ''
  run-tests:
    description: 'Whether to run tests before release'
    required: false
    default: 'false'
  run-linters:
    description: 'Whether to run linters before release'
    required: false
    default: 'true'
  create-release:
    description: 'Whether to create a GitHub release'
    required: false
    default: 'true'
  release-notes:
    description: 'Custom release notes (markdown)'
    required: false
    default: ''
  goreleaser-config:
    description: 'Path to .goreleaser.yml config file'
    required: false
    default: '.goreleaser.yml'
  goreleaser-args:
    description: 'Additional arguments to pass to GoReleaser'
    required: false
    default: ''
  skip-publish:
    description: 'Skip publishing (use --snapshot mode)'
    required: false
    default: 'false'

outputs:
  tag:
    description: 'The release tag that was created'
    value: ${{ steps.tag-output.outputs.tag }}
  sdk-tag:
    description: 'The SDK-specific tag (if applicable)'
    value: ${{ steps.sdk-tag.outputs.sdk-tag }}
  release-url:
    description: 'URL of the created GitHub release'
    value: ${{ steps.goreleaser.outputs.url }}
  artifacts:
    description: 'List of artifacts created by GoReleaser'
    value: ${{ steps.goreleaser.outputs.artifacts }}

runs:
  using: 'composite'
  steps:
    - name: Download protobuf artifacts
      if: inputs.protobuf-artifacts == 'true'
      uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1
      with:
        name: protobuf-generated
        path: proto-generated

    - name: Install system packages
      if: inputs.system-packages != ''
      shell: bash
      env:
        PACKAGES: ${{ inputs.system-packages }}
      run: sudo apt-get update && sudo apt-get install -y $PACKAGES

    - name: Run pre-build script
      if: inputs.pre-build-script != ''
      shell: bash
      run: ${{ inputs.pre-build-script }}

    - name: Detect required Go version from go.mod
      id: detect-go-version
      shell: bash
      run: |
        set -euo pipefail
        MOD_PATH="${{ inputs.module-path }}"
        GO_REQUIRED=""
        if [ -f "$MOD_PATH/go.mod" ]; then
          GO_REQUIRED=$(grep -E '^go [0-9]+\\.[0-9]+' "$MOD_PATH/go.mod" | awk '{print $2}' | head -n1 || true)
        fi
        INPUT_VER="${{ inputs.go-version }}"
        CHOSEN="$INPUT_VER"
        if [ -n "$GO_REQUIRED" ]; then
          HIGHER=$(printf "%s\\n%s\\n" "$INPUT_VER" "$GO_REQUIRED" | sort -V | tail -n1)
          CHOSEN="$HIGHER"
        fi
        echo "version=$CHOSEN" >> "$GITHUB_OUTPUT"

    - name: Set up Go
      uses: actions/setup-go@4a3601121dd01d1626a1e23e37211e3254c1c06c # v6.4.0
      with:
        go-version: ${{ steps.detect-go-version.outputs.version || inputs.go-version }}
        cache-dependency-path: ${{ inputs.module-path }}/go.sum

    - name: Verify Go module
      working-directory: ${{ inputs.module-path }}
      shell: bash
      run: |
        if [ ! -f "go.mod" ]; then
          echo "Error: go.mod not found in ${{ inputs.module-path }}"
          exit 1
        fi
        echo "Module: $(go list -m)"

    - name: Tidy module dependencies
      working-directory: ${{ inputs.module-path }}
      shell: bash
      run: go mod tidy

    - name: Run tests
      if: inputs.run-tests == 'true'
      working-directory: ${{ inputs.module-path }}
      shell: bash
      run: |
        go test -v -race -coverprofile=coverage.out ./...
        go tool cover -func=coverage.out

    - name: Run linters
      if: inputs.run-linters == 'true'
      working-directory: ${{ inputs.module-path }}
      shell: bash
      run: |
        go vet ./...
        if command -v golangci-lint &> /dev/null; then
          golangci-lint run
        fi

    - name: Generate SDK tag
      id: sdk-tag
      if: inputs.is-sdk == 'true' && inputs.sdk-language != ''
      shell: bash
      run: |
        SDK_TAG="${{ inputs.sdk-language }}/${{ inputs.release-version || github.ref_name }}"
        echo "sdk-tag=$SDK_TAG" >> $GITHUB_OUTPUT
        echo "Generated SDK tag: $SDK_TAG"

    - name: Set tag output
      id: tag-output
      shell: bash
      run: |
        MODULE_PATH="${{ inputs.module-path }}"
        TAG_BASE="${{ inputs.release-version || github.ref_name }}"
        if [ "$MODULE_PATH" != "." ]; then
          TAG="${MODULE_PATH}/${TAG_BASE}"
        else
          TAG="${TAG_BASE}"
        fi
        echo "tag=$TAG" >> $GITHUB_OUTPUT
        echo "Generated tag: $TAG"

    - name: Create and push tags
      if: inputs.create-release == 'true'
      shell: bash
      env:
        GITHUB_TOKEN: ${{ inputs.github-token }}
        GOEXPERIMENT: ${{ inputs.go-experiment }}
      run: |
        set -euo pipefail
        TAG="${{ steps.tag-output.outputs.tag }}"

        git config user.name "github-actions[bot]"
        git config user.email "github-actions[bot]@users.noreply.github.com"

        PUSH_URL="https://x-access-token:${GITHUB_TOKEN}@github.com/${GITHUB_REPOSITORY}.git"

        if git rev-parse "$TAG" >/dev/null 2>&1; then
          echo "Tag $TAG already exists, skipping creation"
        else
          git tag -a "$TAG" -m "Release $TAG"
          git push "$PUSH_URL" "$TAG"
          echo "Created and pushed tag: $TAG"
        fi

        if [ "${{ inputs.is-sdk }}" == "true" ] && [ -n "${{ steps.sdk-tag.outputs.sdk-tag }}" ]; then
          SDK_TAG="${{ steps.sdk-tag.outputs.sdk-tag }}"
          if git rev-parse "$SDK_TAG" >/dev/null 2>&1; then
            echo "SDK tag $SDK_TAG already exists, skipping creation"
          else
            git tag -a "$SDK_TAG" -m "SDK Release $SDK_TAG"
            git push "$PUSH_URL" "$SDK_TAG"
            echo "Created and pushed SDK tag: $SDK_TAG"
          fi
        fi

    - name: Run GoReleaser
      id: goreleaser
      uses: goreleaser/goreleaser-action@ec59f474b9834571250b370d4735c50f8e2d1e29 # v7.0.0
      with:
        distribution: goreleaser
        version: v2.13.1
        args: >-
          release --clean
          ${{ inputs.skip-publish == 'true' && '--snapshot' || '' }}
          ${{ inputs.goreleaser-args }}
        workdir: ${{ inputs.module-path }}
      env:
        GITHUB_TOKEN: ${{ inputs.github-token }}
        GOEXPERIMENT: ${{ inputs.go-experiment }}

    - name: Upload Go artifacts
      if: always()
      uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
      with:
        name: go-binaries
        path: dist/**
        if-no-files-found: ignore
        retention-days: 7

    - name: Output summary
      shell: bash
      run: |
        echo "## Go Module Release Summary" >> $GITHUB_STEP_SUMMARY
        echo "" >> $GITHUB_STEP_SUMMARY
        echo "**Module Path:** ${{ inputs.module-path }}" >> $GITHUB_STEP_SUMMARY
        echo "**Tag:** ${{ steps.tag-output.outputs.tag }}" >> $GITHUB_STEP_SUMMARY
        if [ "${{ inputs.is-sdk }}" == "true" ]; then
          echo "**SDK Tag:** ${{ steps.sdk-tag.outputs.sdk-tag }}" >> $GITHUB_STEP_SUMMARY
        fi
        echo "**Go Version:** ${{ steps.detect-go-version.outputs.version }}" >> $GITHUB_STEP_SUMMARY
        if [ "${{ inputs.skip-publish }}" != "true" ]; then
          echo "**Release URL:** ${{ steps.goreleaser.outputs.url }}" >> $GITHUB_STEP_SUMMARY
        fi
"""

# ─── PYTHON ───────────────────────────────────────────────────────────────────
PYTHON_ACTION = """\
# file: action.yml
# version: 2.0.0

name: 'Python Release Build'
description: 'Build Python packages and optionally publish to PyPI'
author: 'falkcorp'

branding:
  icon: 'package'
  color: 'yellow'

inputs:
  # --- orchestration inputs (from wrapper) ---
  python-version:
    description: 'Python version to use'
    required: false
    default: '3.13'
  protobuf-artifacts:
    description: 'Whether protobuf artifacts are available from an earlier job step'
    required: false
    default: 'false'

  # --- implementation inputs (from base) ---
  package-dir:
    description: 'Directory containing setup.py or pyproject.toml'
    required: false
    default: '.'
  build-backend:
    description: 'Build backend (setuptools, poetry, hatchling)'
    required: false
    default: 'setuptools'
  run-tests:
    description: 'Run tests before publishing'
    required: false
    default: 'true'
  test-command:
    description: 'Command to run tests'
    required: false
    default: 'pytest -m "not slow" --maxfail=1 --disable-warnings'
  repository-url:
    description: 'PyPI repository URL'
    required: false
    default: 'https://upload.pypi.org/legacy/'
  pypi-token:
    description: 'PyPI API token (leave empty to skip publish)'
    required: false
    default: ''
  skip-existing:
    description: 'Skip existing packages on PyPI'
    required: false
    default: 'true'
  verify-metadata:
    description: 'Verify package metadata with twine check'
    required: false
    default: 'true'

outputs:
  package-name:
    description: 'Name of the built package'
    value: ${{ steps.build.outputs.package-name }}
  package-version:
    description: 'Version of the built package'
    value: ${{ steps.build.outputs.package-version }}
  wheel-path:
    description: 'Path to built wheel file'
    value: ${{ steps.build.outputs.wheel-path }}

runs:
  using: 'composite'
  steps:
    - name: Download protobuf artifacts
      if: inputs.protobuf-artifacts == 'true'
      uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
      with:
        name: protobuf-generated
        path: proto-generated

    - name: Set up Python
      uses: actions/setup-python@a309ff8b426b58ec0e2a45f0f869d46889d02405 # v6.2.0
      with:
        python-version: ${{ inputs.python-version }}

    - name: Install build tools
      shell: bash
      run: |
        python -m pip install --upgrade pip
        pip install build twine

    - name: Install dependencies
      working-directory: ${{ inputs.package-dir }}
      shell: bash
      run: |
        if [ -f requirements.txt ]; then
          pip install -r requirements.txt
        fi
        if [ -f requirements-dev.txt ]; then
          pip install -r requirements-dev.txt
        fi

    - name: Run tests
      if: inputs.run-tests == 'true'
      working-directory: ${{ inputs.package-dir }}
      shell: bash
      run: ${{ inputs.test-command }}

    - name: Build package
      id: build
      working-directory: ${{ inputs.package-dir }}
      shell: bash
      run: |
        python -m build

        WHEEL_FILE=$(ls dist/*.whl | head -n 1)
        PKG_NAME=$(python -c "import re; print(re.match(r'dist/(.+?)-', '$WHEEL_FILE').group(1))")
        PKG_VERSION=$(python -c "import re; print(re.match(r'dist/.+?-(.+?)-', '$WHEEL_FILE').group(1))")

        echo "package-name=$PKG_NAME" >> $GITHUB_OUTPUT
        echo "package-version=$PKG_VERSION" >> $GITHUB_OUTPUT
        echo "wheel-path=$WHEEL_FILE" >> $GITHUB_OUTPUT

    - name: Verify metadata
      if: inputs.verify-metadata == 'true'
      working-directory: ${{ inputs.package-dir }}
      shell: bash
      run: twine check dist/*

    - name: Publish to PyPI
      if: inputs.pypi-token != '' && inputs.pypi-token != 'dummy-no-publish'
      working-directory: ${{ inputs.package-dir }}
      shell: bash
      env:
        TWINE_USERNAME: __token__
        TWINE_PASSWORD: ${{ inputs.pypi-token }}
        TWINE_REPOSITORY_URL: ${{ inputs.repository-url }}
      run: |
        SKIP_ARG=""
        if [ "${{ inputs.skip-existing }}" == "true" ]; then
          SKIP_ARG="--skip-existing"
        fi
        twine upload $SKIP_ARG dist/*

    - name: Upload Python dist artifacts
      if: always()
      uses: actions/upload-artifact@b7c566a772e6b6bfb58ed0dc250532a479d7789f # v6.0.0
      with:
        name: python-dist
        path: dist/**
        if-no-files-found: ignore
        retention-days: 14

    - name: Output summary
      shell: bash
      run: |
        echo "## Python Package Release Summary" >> $GITHUB_STEP_SUMMARY
        echo "" >> $GITHUB_STEP_SUMMARY
        echo "**Package:** ${{ steps.build.outputs.package-name }}" >> $GITHUB_STEP_SUMMARY
        echo "**Version:** ${{ steps.build.outputs.package-version }}" >> $GITHUB_STEP_SUMMARY
        echo "**Python:** ${{ inputs.python-version }}" >> $GITHUB_STEP_SUMMARY
        if [ -n "${{ inputs.pypi-token }}" ] && [ "${{ inputs.pypi-token }}" != "dummy-no-publish" ]; then
          echo "**Repository:** ${{ inputs.repository-url }}" >> $GITHUB_STEP_SUMMARY
        else
          echo "**Published:** no (no pypi-token provided)" >> $GITHUB_STEP_SUMMARY
        fi
"""

# ─── RUST ─────────────────────────────────────────────────────────────────────
RUST_ACTION = """\
# file: action.yml
# version: 2.0.0

name: 'Rust Release Build'
description: 'Build, test, lint, and optionally publish Rust crates'
author: 'falkcorp'

branding:
  icon: 'package'
  color: 'orange'

inputs:
  # --- orchestration inputs (from wrapper) ---
  release-version:
    description: 'Release version tag (informational, passed to crate metadata)'
    required: false
    default: ''
  protobuf-artifacts:
    description: 'Whether protobuf artifacts are available from an earlier job step'
    required: false
    default: 'false'

  # --- implementation inputs (from base) ---
  rust-version:
    description: 'Rust toolchain version to use'
    required: false
    default: 'stable'
  manifest-path:
    description: 'Path to Cargo.toml'
    required: false
    default: './Cargo.toml'
  crate-token:
    description: 'crates.io API token (leave empty to skip publish)'
    required: false
    default: ''
  run-tests:
    description: 'Run tests before publishing'
    required: false
    default: 'true'
  run-clippy:
    description: 'Run clippy linter'
    required: false
    default: 'true'
  check-format:
    description: 'Check code formatting with rustfmt'
    required: false
    default: 'true'
  dry-run:
    description: 'Perform a dry run (do not actually publish to crates.io)'
    required: false
    default: 'true'
  features:
    description: 'Cargo features to enable (comma-separated)'
    required: false
    default: ''
  all-features:
    description: 'Enable all Cargo features'
    required: false
    default: 'false'

outputs:
  crate-name:
    description: 'Name of the built crate'
    value: ${{ steps.metadata.outputs.name }}
  crate-version:
    description: 'Version of the built crate'
    value: ${{ steps.metadata.outputs.version }}

runs:
  using: 'composite'
  steps:
    - name: Download protobuf artifacts
      if: inputs.protobuf-artifacts == 'true'
      uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
      with:
        name: protobuf-generated
        path: proto-generated

    - name: Set up Rust
      uses: actions-rust-lang/setup-rust-toolchain@2b1f5e9b395427c92ee4e3331786ca3c37afe2d7 # v1.16.0
      with:
        toolchain: ${{ inputs.rust-version }}
        components: clippy, rustfmt

    - name: Extract crate metadata
      id: metadata
      shell: bash
      run: |
        MANIFEST="${{ inputs.manifest-path }}"
        NAME=$(cargo metadata --manifest-path $MANIFEST \\
          --format-version 1 --no-deps | jq -r '.packages[0].name')
        VERSION=$(cargo metadata --manifest-path $MANIFEST \\
          --format-version 1 --no-deps | jq -r '.packages[0].version')
        echo "name=$NAME" >> $GITHUB_OUTPUT
        echo "version=$VERSION" >> $GITHUB_OUTPUT

    - name: Check formatting
      if: inputs.check-format == 'true'
      shell: bash
      run: cargo fmt --manifest-path ${{ inputs.manifest-path }} -- --check

    - name: Run clippy
      if: inputs.run-clippy == 'true'
      shell: bash
      run: |
        FEATURES_ARG=""
        if [ "${{ inputs.all-features }}" == "true" ]; then
          FEATURES_ARG="--all-features"
        elif [ -n "${{ inputs.features }}" ]; then
          FEATURES_ARG="--features ${{ inputs.features }}"
        fi
        cargo clippy --manifest-path ${{ inputs.manifest-path }} $FEATURES_ARG -- -D warnings

    - name: Run tests
      if: inputs.run-tests == 'true'
      shell: bash
      run: |
        FEATURES_ARG=""
        if [ "${{ inputs.all-features }}" == "true" ]; then
          FEATURES_ARG="--all-features"
        elif [ -n "${{ inputs.features }}" ]; then
          FEATURES_ARG="--features ${{ inputs.features }}"
        fi
        cargo test --manifest-path ${{ inputs.manifest-path }} $FEATURES_ARG

    - name: Build crate
      shell: bash
      run: |
        FEATURES_ARG=""
        if [ "${{ inputs.all-features }}" == "true" ]; then
          FEATURES_ARG="--all-features"
        elif [ -n "${{ inputs.features }}" ]; then
          FEATURES_ARG="--features ${{ inputs.features }}"
        fi
        cargo build --manifest-path ${{ inputs.manifest-path }} --release $FEATURES_ARG

    - name: Publish to crates.io
      if: inputs.crate-token != '' && inputs.crate-token != 'dummy-no-publish'
      shell: bash
      env:
        CARGO_REGISTRY_TOKEN: ${{ inputs.crate-token }}
      run: |
        DRY_RUN=""
        if [ "${{ inputs.dry-run }}" == "true" ]; then
          DRY_RUN="--dry-run"
        fi
        FEATURES_ARG=""
        if [ "${{ inputs.all-features }}" == "true" ]; then
          FEATURES_ARG="--all-features"
        elif [ -n "${{ inputs.features }}" ]; then
          FEATURES_ARG="--features ${{ inputs.features }}"
        fi
        cargo publish --manifest-path ${{ inputs.manifest-path }} $DRY_RUN $FEATURES_ARG --allow-dirty

    - name: Upload Rust artifacts
      if: always()
      uses: actions/upload-artifact@b7c566a772e6b6bfb58ed0dc250532a479d7789f # v6.0.0
      with:
        name: rust-build
        path: target/release/*
        if-no-files-found: ignore
        retention-days: 14

    - name: Output summary
      shell: bash
      run: |
        {
          echo "## Rust Crate Release Summary"
          echo ""
          echo "**Crate:** ${{ steps.metadata.outputs.name }}"
          echo "**Version:** ${{ steps.metadata.outputs.version }}"
          echo "**Rust:** ${{ inputs.rust-version }}"
          if [ "${{ inputs.dry-run }}" == "true" ]; then
            echo "**Status:** Dry run (not published)"
          elif [ -z "${{ inputs.crate-token }}" ] || [ "${{ inputs.crate-token }}" == "dummy-no-publish" ]; then
            echo "**Status:** Not published (no crate-token)"
          else
            echo "**Registry:** crates.io"
          fi
        } >> $GITHUB_STEP_SUMMARY
"""

# ─── DOCKER ───────────────────────────────────────────────────────────────────
DOCKER_ACTION = """\
# file: action.yml
# version: 2.0.0

name: 'Docker Release Build'
description: 'Build and push multi-platform Docker images to a registry'
author: 'falkcorp'

branding:
  icon: 'package'
  color: 'blue'

inputs:
  # --- simplified facade inputs (from wrapper) ---
  platform:
    description: 'Target platform (e.g. linux/amd64). Overrides platforms if set.'
    required: false
    default: ''
  registry:
    description: 'Container registry hostname'
    required: true
  image-name:
    description: 'Container image name (without registry prefix)'
    required: true
  username:
    description: 'Registry username'
    required: true
  password:
    description: 'Registry password or token'
    required: true

  # --- full implementation inputs (from base) ---
  context:
    description: 'Docker build context directory'
    required: false
    default: '.'
  dockerfile:
    description: 'Path to Dockerfile'
    required: false
    default: 'Dockerfile'
  platforms:
    description: 'Comma-separated list of platforms (used when platform is empty)'
    required: false
    default: 'linux/amd64,linux/arm64'
  tag:
    description: 'Primary image tag (defaults to git SHA)'
    required: false
    default: ''
  additional-tags:
    description: 'Additional tags to apply (comma-separated)'
    required: false
    default: ''
  push:
    description: 'Whether to push the image'
    required: false
    default: 'true'
  build-args:
    description: 'Build arguments (key=value, one per line)'
    required: false
    default: ''
  cache-from:
    description: 'Cache source (type=registry,ref=...)'
    required: false
    default: ''
  cache-to:
    description: 'Cache destination (type=registry,ref=...)'
    required: false
    default: ''

outputs:
  digest:
    description: 'Image digest'
    value: ${{ steps.build.outputs.digest }}
  metadata:
    description: 'Build result metadata'
    value: ${{ steps.build.outputs.metadata }}
  tags:
    description: 'Generated tags'
    value: ${{ steps.meta.outputs.tags }}

runs:
  using: 'composite'
  steps:
    - name: Set up QEMU
      uses: docker/setup-qemu-action@ce360397dd3f832beb865e1373c09c0e9f86d70a # v4.0.0

    - name: Set up Docker Buildx
      uses: docker/setup-buildx-action@4d04d5d9486b7bd6fa91e7baf45bbb4f8b9deedd # v4.0.0

    - name: Log in to registry
      if: inputs.username != '' && inputs.password != ''
      uses: docker/login-action@4907a6ddec9925e35a0a9e82d7399ccc52663121 # v4.1.0
      with:
        registry: ${{ inputs.registry }}
        username: ${{ inputs.username }}
        password: ${{ inputs.password }}

    - name: Resolve effective platforms
      id: platforms
      shell: bash
      run: |
        # Single-platform shorthand takes precedence over multi-platform list
        if [ -n "${{ inputs.platform }}" ]; then
          echo "value=${{ inputs.platform }}" >> "$GITHUB_OUTPUT"
        else
          echo "value=${{ inputs.platforms }}" >> "$GITHUB_OUTPUT"
        fi

    - name: Resolve image tag
      id: resolved-tag
      shell: bash
      run: |
        TAG="${{ inputs.tag }}"
        if [ -z "$TAG" ]; then
          TAG="${{ github.sha }}"
        fi
        echo "value=$TAG" >> "$GITHUB_OUTPUT"

    - name: Resolve additional tags
      id: resolved-extra
      shell: bash
      run: |
        EXTRA="${{ inputs.additional-tags }}"
        if [ -z "$EXTRA" ] && [[ "${{ github.ref }}" == refs/tags/* ]]; then
          EXTRA="${{ github.ref_name }}"
        fi
        echo "value=$EXTRA" >> "$GITHUB_OUTPUT"

    - name: Extract metadata
      id: meta
      uses: docker/metadata-action@030e881283bb7a6894de51c315a6bfe6a94e05cf # v6.0.0
      with:
        images: ${{ inputs.registry }}/${{ inputs.image-name }}
        tags: |
          type=raw,value=${{ steps.resolved-tag.outputs.value }}
          ${{ steps.resolved-extra.outputs.value }}

    - name: Build and push
      id: build
      uses: docker/build-push-action@bcafcacb16a39f128d818304e6c9c0c18556b85f # v7.1.0
      with:
        context: ${{ inputs.context }}
        file: ${{ inputs.dockerfile }}
        platforms: ${{ steps.platforms.outputs.value }}
        push: ${{ inputs.push }}
        tags: ${{ steps.meta.outputs.tags }}
        labels: ${{ steps.meta.outputs.labels }}
        build-args: ${{ inputs.build-args }}
        cache-from: ${{ inputs.cache-from }}
        cache-to: ${{ inputs.cache-to }}

    - name: Output summary
      shell: bash
      run: |
        echo "## Docker Build Summary" >> $GITHUB_STEP_SUMMARY
        echo "" >> $GITHUB_STEP_SUMMARY
        echo "**Image:** ${{ inputs.registry }}/${{ inputs.image-name }}" >> $GITHUB_STEP_SUMMARY
        echo "**Tags:** ${{ steps.meta.outputs.tags }}" >> $GITHUB_STEP_SUMMARY
        echo "**Platforms:** ${{ steps.platforms.outputs.value }}" >> $GITHUB_STEP_SUMMARY
        echo "**Digest:** ${{ steps.build.outputs.digest }}" >> $GITHUB_STEP_SUMMARY
"""

# ─── FRONTEND ─────────────────────────────────────────────────────────────────
FRONTEND_ACTION = """\
# file: action.yml
# version: 2.0.0

name: 'Frontend Release Build'
description: 'Build frontend applications (React, Vue, Angular, etc.) with auto-detection'
author: 'falkcorp'

branding:
  icon: 'globe'
  color: 'purple'

inputs:
  # --- orchestration inputs (from wrapper) ---
  node-version:
    description: 'Node.js version to use'
    required: false
    default: '22'

  # --- implementation inputs (from base) ---
  package-manager:
    description: 'Package manager (npm, yarn, pnpm)'
    required: false
    default: 'npm'
  working-directory:
    description: >
      Working directory for the project. Defaults to auto-detect:
      checks web/, frontend/, then . in that order.
    required: false
    default: ''
  build-command:
    description: 'Build command'
    required: false
    default: 'npm run build'
  test-command:
    description: 'Test command'
    required: false
    default: 'npm test'
  lint-command:
    description: 'Lint command'
    required: false
    default: 'npm run lint'
  run-tests:
    description: 'Run tests before building'
    required: false
    default: 'true'
  run-lint:
    description: 'Run linter before building'
    required: false
    default: 'true'
  build-output-dir:
    description: 'Build output directory (relative to working-directory)'
    required: false
    default: 'dist'
  create-artifact:
    description: 'Upload build output as a GitHub Actions artifact'
    required: false
    default: 'true'
  artifact-name:
    description: 'Artifact name'
    required: false
    default: 'frontend-build'
  artifact-retention-days:
    description: 'Artifact retention days'
    required: false
    default: '90'

outputs:
  build-path:
    description: 'Path to build output'
    value: ${{ steps.build-info.outputs.path }}
  artifact-url:
    description: 'URL of the uploaded artifact'
    value: ${{ steps.upload.outputs.artifact-url }}

runs:
  using: 'composite'
  steps:
    - name: Detect frontend directory
      id: frontend-dir
      shell: bash
      run: |
        # Respect explicit working-directory; otherwise auto-detect
        if [ -n "${{ inputs.working-directory }}" ]; then
          echo "dir=${{ inputs.working-directory }}" >> "$GITHUB_OUTPUT"
        elif [ -f "web/package.json" ]; then
          echo "dir=web" >> "$GITHUB_OUTPUT"
        elif [ -f "frontend/package.json" ]; then
          echo "dir=frontend" >> "$GITHUB_OUTPUT"
        elif [ -f "package.json" ]; then
          echo "dir=." >> "$GITHUB_OUTPUT"
        else
          echo "dir=." >> "$GITHUB_OUTPUT"
        fi

    - name: Detect dependency lock file
      id: dependency-file
      shell: bash
      run: |
        set -euo pipefail
        WORKDIR="${{ steps.frontend-dir.outputs.dir }}"
        MANAGER="${{ inputs.package-manager }}"
        case "$MANAGER" in
          npm)
            CANDIDATES=("${WORKDIR}/package-lock.json" "${WORKDIR}/npm-shrinkwrap.json") ;;
          yarn)
            CANDIDATES=("${WORKDIR}/yarn.lock") ;;
          pnpm)
            CANDIDATES=("${WORKDIR}/pnpm-lock.yaml") ;;
          *)
            echo "::error::Unsupported package manager: ${MANAGER}"; exit 1 ;;
        esac
        for file in "${CANDIDATES[@]}"; do
          if [ -f "$file" ]; then
            echo "path=$file" >> "$GITHUB_OUTPUT"
            exit 0
          fi
        done
        echo "::notice::No dependency lock file found; skipping cache setup."
        echo "path=" >> "$GITHUB_OUTPUT"

    - name: Set up Node.js (with cache)
      if: steps.dependency-file.outputs.path != ''
      uses: actions/setup-node@53b83947a5a98c8d113130e565377fae1a50d02f # v6.3.0
      with:
        node-version: ${{ inputs.node-version }}
        cache: ${{ inputs.package-manager }}
        cache-dependency-path: ${{ steps.dependency-file.outputs.path }}

    - name: Set up Node.js (no cache)
      if: steps.dependency-file.outputs.path == ''
      uses: actions/setup-node@53b83947a5a98c8d113130e565377fae1a50d02f # v6.3.0
      with:
        node-version: ${{ inputs.node-version }}

    - name: Install dependencies
      working-directory: ${{ steps.frontend-dir.outputs.dir }}
      shell: bash
      run: |
        set -euo pipefail
        LOCK_PATH="${{ steps.dependency-file.outputs.path }}"
        MANAGER="${{ inputs.package-manager }}"
        if [ "$MANAGER" = "npm" ]; then
          [ -n "$LOCK_PATH" ] && npm ci || npm install --no-audit --no-fund
        elif [ "$MANAGER" = "yarn" ]; then
          [ -n "$LOCK_PATH" ] && yarn install --frozen-lockfile || yarn install --non-interactive
        elif [ "$MANAGER" = "pnpm" ]; then
          [ -n "$LOCK_PATH" ] && pnpm install --frozen-lockfile || pnpm install
        else
          echo "Unsupported package manager: $MANAGER"; exit 1
        fi

    - name: Run linter
      if: inputs.run-lint == 'true'
      continue-on-error: true
      working-directory: ${{ steps.frontend-dir.outputs.dir }}
      shell: bash
      run: ${{ inputs.lint-command }}

    - name: Run tests
      if: inputs.run-tests == 'true'
      continue-on-error: true
      working-directory: ${{ steps.frontend-dir.outputs.dir }}
      shell: bash
      run: ${{ inputs.test-command }}

    - name: Build application (production)
      working-directory: ${{ steps.frontend-dir.outputs.dir }}
      shell: bash
      env:
        NODE_ENV: production
      run: ${{ inputs.build-command }}

    - name: Set build info
      id: build-info
      shell: bash
      run: |
        BUILD_PATH="${{ steps.frontend-dir.outputs.dir }}/${{ inputs.build-output-dir }}"
        echo "path=$BUILD_PATH" >> $GITHUB_OUTPUT

    - name: Upload build artifact
      id: upload
      if: inputs.create-artifact == 'true'
      uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
      with:
        name: ${{ inputs.artifact-name }}
        path: ${{ steps.build-info.outputs.path }}
        if-no-files-found: ignore
        retention-days: ${{ inputs.artifact-retention-days }}

    - name: Output summary
      shell: bash
      run: |
        echo "## Frontend Build Summary" >> $GITHUB_STEP_SUMMARY
        echo "" >> $GITHUB_STEP_SUMMARY
        echo "**Node.js:** ${{ inputs.node-version }}" >> $GITHUB_STEP_SUMMARY
        echo "**Package Manager:** ${{ inputs.package-manager }}" >> $GITHUB_STEP_SUMMARY
        echo "**Working Directory:** ${{ steps.frontend-dir.outputs.dir }}" >> $GITHUB_STEP_SUMMARY
        echo "**Build Output:** ${{ steps.build-info.outputs.path }}" >> $GITHUB_STEP_SUMMARY
        if [ "${{ inputs.create-artifact }}" == "true" ]; then
          echo "**Artifact:** ${{ inputs.artifact-name }}" >> $GITHUB_STEP_SUMMARY
          echo "**Retention:** ${{ inputs.artifact-retention-days }} days" >> $GITHUB_STEP_SUMMARY
        fi
"""

ACTIONS = {
    "go": (WRAPPER_DIRS["go"], GO_ACTION),
    "python": (WRAPPER_DIRS["python"], PYTHON_ACTION),
    "rust": (WRAPPER_DIRS["rust"], RUST_ACTION),
    "docker": (WRAPPER_DIRS["docker"], DOCKER_ACTION),
    "frontend": (WRAPPER_DIRS["frontend"], FRONTEND_ACTION),
}


def commit_with_retry(repo_dir, msg):
    """Commit, retrying once if pre-commit hook (prettier) modifies files."""
    for attempt in range(3):
        result = subprocess.run(
            ["git", "commit", "-m", msg], cwd=repo_dir, capture_output=True, text=True
        )
        if result.returncode == 0:
            return
        if "modified by this hook" in result.stdout + result.stderr or attempt == 0:
            subprocess.run(["git", "add", "action.yml"], cwd=repo_dir)
            continue
        raise RuntimeError(f"Commit failed: {result.stderr}")


def main():
    for lang, (repo_dir, content) in ACTIONS.items():
        print(f"\n{'=' * 60}")
        print(f"  Consolidating gha-release-{lang}")
        print(f"  Dir: {repo_dir}")
        print("=" * 60)

        # Ensure we're on chore/falkcorp-migration
        current = run(["git", "rev-parse", "--abbrev-ref", "HEAD"], cwd=repo_dir)
        if current != "chore/falkcorp-migration":
            run(["git", "checkout", "chore/falkcorp-migration"], cwd=repo_dir)

        # Write consolidated action.yml
        action_path = os.path.join(repo_dir, "action.yml")
        with open(action_path, "w") as f:
            f.write(content)
        print(f"  Wrote {action_path} ({len(content)} bytes)")

        # Stage and commit
        run(["git", "add", "action.yml"], cwd=repo_dir)

        # Check if there's anything to commit
        status = run(["git", "status", "--short"], cwd=repo_dir)
        if not status:
            print("  SKIP commit (no changes)")
            continue

        commit_with_retry(
            repo_dir,
            "feat: consolidate action — inline full implementation, remove cross-repo uses chain",
        )

        # Push
        result = subprocess.run(
            ["git", "push", "origin", "chore/falkcorp-migration"],
            cwd=repo_dir,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            print(f"  Push stderr: {result.stderr}")
            raise RuntimeError("Push failed")
        print("  Pushed chore/falkcorp-migration")

    print("\n\nAll 5 actions consolidated and pushed.")
    print("PRs updated automatically (existing chore/falkcorp-migration PRs).")
    print("\nNext: archive jdfalk/gha-release-* base repos (jdfalk/release-go-action etc.)")


if __name__ == "__main__":
    main()

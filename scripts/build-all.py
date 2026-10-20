#!/usr/bin/env python3
# file: scripts/build-all.py
# version: 1.0.1
# guid: 9a8b7c6d-5e4f-3d2c-1b0a-9f8e7d6c5b4a

"""
Build script for audiobook-organizer
Builds all platform/architecture combinations with optional frontend embedding
"""

import argparse
import os
import subprocess
import sys
from pathlib import Path

# Build configurations: (GOOS, GOARCH, output_suffix)
PLATFORMS = [
    ("linux", "amd64", "linux-amd64"),
    ("linux", "arm64", "linux-arm64"),
    ("darwin", "amd64", "macos-amd64"),
    ("darwin", "arm64", "macos-arm64"),
    ("windows", "amd64", "windows-amd64.exe"),
]


def run_command(cmd: list[str], cwd: Path = None, env: dict = None) -> tuple[int, str, str]:
    """Run a command and return exit code, stdout, stderr"""
    result = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True)
    return result.returncode, result.stdout, result.stderr


def build_frontend(web_dir: Path) -> bool:
    """Build the frontend using npm"""
    print("Building frontend...")

    if not (web_dir / "package.json").exists():
        print(f"Error: package.json not found in {web_dir}")
        return False

    # Run npm build
    code, stdout, stderr = run_command(["npm", "run", "build"], cwd=web_dir)

    if code != 0:
        print("Frontend build failed:")
        print(stderr)
        return False

    print("✓ Frontend built successfully")
    return True


def build_binary(
    goos: str,
    goarch: str,
    output_name: str,
    repo_root: Path,
    embed_frontend: bool,
    output_dir: Path,
) -> bool:
    """Build a single binary"""

    build_tags = ["-tags", "embed_frontend"] if embed_frontend else []
    suffix = "-embedded" if embed_frontend else ""
    output_path = output_dir / f"audiobook-organizer-{output_name}{suffix}"

    print(f"Building {output_name}{suffix}...")

    env = os.environ.copy()
    env["GOOS"] = goos
    env["GOARCH"] = goarch
    env["CGO_ENABLED"] = "0"

    cmd = ["go", "build"] + build_tags + ["-o", str(output_path)]

    code, stdout, stderr = run_command(cmd, cwd=repo_root, env=env)

    if code != 0:
        print(f"✗ Build failed for {output_name}{suffix}:")
        print(stderr)
        return False

    # Get file size
    size_mb = output_path.stat().st_size / (1024 * 1024)
    print(f"✓ Built {output_path.name} ({size_mb:.1f} MB)")
    return True


def main():
    parser = argparse.ArgumentParser(description="Build audiobook-organizer for all platforms")
    parser.add_argument(
        "--embed-frontend",
        action="store_true",
        help="Build with embedded frontend (requires npm build first)",
    )
    parser.add_argument(
        "--no-embed-frontend",
        action="store_true",
        help="Build without embedded frontend (API-only binaries)",
    )
    parser.add_argument(
        "--skip-frontend-build",
        action="store_true",
        help="Skip rebuilding frontend (use existing web/dist)",
    )
    parser.add_argument(
        "--platforms",
        nargs="+",
        choices=[p[2] for p in PLATFORMS],
        help="Build only specific platforms",
    )
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=Path("bin"),
        help="Output directory for binaries (default: bin)",
    )

    args = parser.parse_args()

    # Determine which builds to do
    build_embedded = args.embed_frontend or not args.no_embed_frontend
    build_non_embedded = args.no_embed_frontend or not args.embed_frontend

    # Get repository root
    script_dir = Path(__file__).parent
    repo_root = script_dir.parent
    web_dir = repo_root / "web"

    # Create output directory
    args.output_dir.mkdir(parents=True, exist_ok=True)

    print(f"Repository root: {repo_root}")
    print(f"Output directory: {args.output_dir}")
    print(f"Build embedded: {build_embedded}")
    print(f"Build non-embedded: {build_non_embedded}")
    print()

    # Build frontend if needed
    if build_embedded and not args.skip_frontend_build:
        if not build_frontend(web_dir):
            print("\nFrontend build failed, cannot build embedded binaries")
            return 1
        print()

    # Filter platforms if specified
    platforms_to_build = PLATFORMS
    if args.platforms:
        platforms_to_build = [p for p in PLATFORMS if p[2] in args.platforms]

    # Build binaries
    failed_builds = []
    successful_builds = []

    for goos, goarch, output_suffix in platforms_to_build:
        if build_non_embedded:
            if build_binary(goos, goarch, output_suffix, repo_root, False, args.output_dir):
                successful_builds.append(f"{output_suffix} (non-embedded)")
            else:
                failed_builds.append(f"{output_suffix} (non-embedded)")

        if build_embedded:
            if build_binary(goos, goarch, output_suffix, repo_root, True, args.output_dir):
                successful_builds.append(f"{output_suffix} (embedded)")
            else:
                failed_builds.append(f"{output_suffix} (embedded)")

    # Summary
    print("\n" + "=" * 60)
    print("BUILD SUMMARY")
    print("=" * 60)

    if successful_builds:
        print(f"\n✓ Successful builds ({len(successful_builds)}):")
        for build in successful_builds:
            print(f"  - {build}")

    if failed_builds:
        print(f"\n✗ Failed builds ({len(failed_builds)}):")
        for build in failed_builds:
            print(f"  - {build}")
        return 1

    print("\n✓ All builds completed successfully!")
    print(f"Binaries are in: {args.output_dir.absolute()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

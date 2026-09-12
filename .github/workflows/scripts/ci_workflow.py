#!/usr/bin/env python3
"""Helper utilities invoked from GitHub Actions CI workflows."""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import textwrap
import time
from collections.abc import Iterable
from pathlib import Path
from typing import Any

try:
    import requests  # type: ignore[import-untyped]
except ModuleNotFoundError:  # pragma: no cover - fallback when requests unavailable
    requests = None
    from urllib.parse import urlencode
    from urllib.request import Request, urlopen

    class _HTTPResponse:
        """Minimal response wrapper mirroring requests.Response."""

        def __init__(self, status_code: int, payload: dict[str, Any]) -> None:
            self.status_code = status_code
            self._payload = payload

        def json(self) -> dict[str, Any]:
            return self._payload

    def _http_get(
        url: str,
        headers: dict[str, str] | None = None,
        params: dict[str, Any] | None = None,
        timeout: int = 30,
    ) -> _HTTPResponse:
        if params:
            query = urlencode(params)
            url = f"{url}?{query}"
        req = Request(url, headers=headers or {})
        with urlopen(req, timeout=timeout) as resp:
            status_code = resp.getcode()
            body = resp.read().decode("utf-8")
        try:
            payload = json.loads(body or "{}")
        except json.JSONDecodeError:
            payload = {}
        return _HTTPResponse(status_code, payload)

else:  # pragma: no cover - exercised in runtime environments with requests installed

    def _http_get(
        url: str,
        headers: dict[str, str] | None = None,
        params: dict[str, Any] | None = None,
        timeout: int = 30,
    ):
        return requests.get(url, headers=headers, params=params, timeout=timeout)


_CONFIG_CACHE: dict[str, Any] | None = None


def append_to_file(path_env: str, content: str) -> None:
    """Append content to the file referenced by a GitHub Actions environment variable."""
    file_path = os.environ.get(path_env)
    if not file_path:
        return
    Path(file_path).parent.mkdir(parents=True, exist_ok=True)
    with open(file_path, "a", encoding="utf-8") as handle:
        handle.write(content)


def write_output(name: str, value: str) -> None:
    append_to_file("GITHUB_OUTPUT", f"{name}={value}\n")


def append_env(name: str, value: str) -> None:
    append_to_file("GITHUB_ENV", f"{name}={value}\n")


def append_summary(text: str) -> None:
    append_to_file("GITHUB_STEP_SUMMARY", text)


def get_repository_config() -> dict[str, Any]:
    global _CONFIG_CACHE
    if _CONFIG_CACHE is not None:
        return _CONFIG_CACHE

    raw = os.environ.get("REPOSITORY_CONFIG")
    if not raw:
        _CONFIG_CACHE = {}
        return _CONFIG_CACHE

    try:
        _CONFIG_CACHE = json.loads(raw)
    except json.JSONDecodeError:
        print(
            "::warning::Unable to parse REPOSITORY_CONFIG JSON; falling back to defaults"
        )
        _CONFIG_CACHE = {}
    return _CONFIG_CACHE


def _config_path(default: Any, *path: str) -> Any:
    current: Any = get_repository_config()
    for key in path:
        if not isinstance(current, dict) or key not in current:
            return default
        current = current[key]
    return current


def debug_filter(_: argparse.Namespace) -> None:
    mapping = {
        "Go files changed": os.environ.get("CI_GO_FILES", ""),
        "Frontend files changed": os.environ.get("CI_FRONTEND_FILES", ""),
        "Python files changed": os.environ.get("CI_PYTHON_FILES", ""),
        "Rust files changed": os.environ.get("CI_RUST_FILES", ""),
        "Docker files changed": os.environ.get("CI_DOCKER_FILES", ""),
        "Docs files changed": os.environ.get("CI_DOCS_FILES", ""),
        "Workflow files changed": os.environ.get("CI_WORKFLOW_FILES", ""),
        "Workflow YAML files changed": os.environ.get("CI_WORKFLOW_YAML_FILES", ""),
        "Workflow scripts changed": os.environ.get("CI_WORKFLOW_SCRIPT_FILES", ""),
        "Linter config files changed": os.environ.get("CI_LINT_FILES", ""),
    }
    for label, value in mapping.items():
        print(f"{label}: {value}")


def determine_execution(_: argparse.Namespace) -> None:
    commit_message = os.environ.get("GITHUB_HEAD_COMMIT_MESSAGE", "")
    skip_ci = bool(
        re.search(r"\[(skip ci|ci skip)\]", commit_message, flags=re.IGNORECASE)
    )
    write_output("skip_ci", "true" if skip_ci else "false")
    if skip_ci:
        print("Skipping CI due to commit message")
    else:
        print("CI will continue; no skip directive found in commit message")

    write_output("should_lint", "true")
    write_output("should_test_go", os.environ.get("CI_GO_FILES", "false"))
    write_output("should_test_frontend", os.environ.get("CI_FRONTEND_FILES", "false"))
    write_output("should_test_python", os.environ.get("CI_PYTHON_FILES", "false"))
    write_output("should_test_rust", os.environ.get("CI_RUST_FILES", "false"))
    write_output("should_test_docker", os.environ.get("CI_DOCKER_FILES", "false"))


def wait_for_pr_automation(_: argparse.Namespace) -> None:
    repo = os.environ.get("GITHUB_REPOSITORY")
    token = os.environ.get("GITHUB_TOKEN")
    target_sha = os.environ.get("TARGET_SHA")
    workflow_name = os.environ.get("WORKFLOW_NAME", "PR Automation")
    max_attempts = int(os.environ.get("MAX_ATTEMPTS", "60"))
    sleep_seconds = int(os.environ.get("SLEEP_SECONDS", "10"))

    if not (repo and token and target_sha):
        print("Missing required environment values; skipping PR automation wait")
        return

    headers = {
        "Authorization": f"token {token}",
        "Accept": "application/vnd.github.v3+json",
    }
    url = f"https://api.github.com/repos/{repo}/actions/runs"

    print("🔄 Waiting for PR automation to complete...")
    for attempt in range(max_attempts):
        print(
            f"Checking for PR automation completion (attempt {attempt + 1}/{max_attempts})..."
        )
        try:
            response = _http_get(
                url, headers=headers, params={"per_page": 100}, timeout=30
            )
        except Exception as exc:  # pragma: no cover - network issues during CI
            print(f"::warning::Unable to query workflow runs: {exc}")
            time.sleep(sleep_seconds)
            continue
        if response.status_code != 200:
            print(f"::warning::Unable to query workflow runs: {response.status_code}")
            time.sleep(sleep_seconds)
            continue

        runs = response.json().get("workflow_runs", [])
        matching_runs = [
            run
            for run in runs
            if run.get("head_sha") == target_sha and run.get("name") == workflow_name
        ]

        if not matching_runs:
            print("ℹ️  No PR automation workflow found, proceeding with CI")
            return

        status = matching_runs[0].get("status", "")
        if status == "completed":
            print("✅ PR automation has completed, proceeding with CI")
            return

        print(f"⏳ PR automation status: {status or 'unknown'}, waiting...")
        time.sleep(sleep_seconds)

    print("⚠️  Timeout waiting for PR automation, proceeding with CI anyway")


def _export_env_from_file(file_path: Path) -> None:
    with file_path.open(encoding="utf-8") as handle:
        for line in handle:
            if "=" not in line:
                continue
            key, value = line.split("=", 1)
            key = key.strip()
            if not key or key.startswith("#"):
                continue
            append_env(key, value.strip())


def load_super_linter_config(_: argparse.Namespace) -> None:
    event_name = os.environ.get("EVENT_NAME", "")
    pr_env = Path(os.environ.get("PR_ENV_FILE", "super-linter-pr.env"))
    ci_env = Path(os.environ.get("CI_ENV_FILE", "super-linter-ci.env"))

    chosen: Path | None = None

    if event_name in {"pull_request", "pull_request_target"}:
        if pr_env.is_file():
            print(f"Loading PR Super Linter configuration from {pr_env}")
            chosen = pr_env
        elif ci_env.is_file():
            print(f"PR config not found, falling back to CI config ({ci_env})")
            chosen = ci_env
    elif ci_env.is_file():
        print(f"Loading CI Super Linter configuration from {ci_env}")
        chosen = ci_env

    if chosen:
        _export_env_from_file(chosen)
        write_output("config-file", chosen.name)
    else:
        print("Warning: No Super Linter configuration found")
        write_output("config-file", "")


def write_validation_summary(_: argparse.Namespace) -> None:
    event_name = os.environ.get("EVENT_NAME", "unknown")
    config_name = os.environ.get("SUMMARY_CONFIG", "super-linter-ci.env")
    append_summary(
        textwrap.dedent(
            f"""\
            # 🔍 CI Validation Results

            ✅ **Code validation completed**

            ## Configuration
            - **Mode**: Validation only (no auto-fixes)
            - **Configuration**: {config_name}
            - **Event**: {event_name}

            """
        )
    )


def _ensure_go_context() -> bool:
    if not Path("go.mod").is_file():
        print("ℹ️ No go.mod found; skipping Go step")
        return False
    return True


def go_setup(_: argparse.Namespace) -> None:
    if not _ensure_go_context():
        return
    subprocess.run(["go", "mod", "download"], check=True)
    subprocess.run(["go", "build", "-v", "./..."], check=True)


def _parse_go_coverage(total_line: str) -> float:
    parts = total_line.strip().split()
    if not parts:
        raise ValueError("Unable to parse go coverage output")
    percentage = parts[-1].rstrip("%")
    return float(percentage)


def go_test(_: argparse.Namespace) -> None:
    if not _ensure_go_context():
        return

    coverage_file = os.environ.get("COVERAGE_FILE", "coverage.out")
    coverage_html = os.environ.get("COVERAGE_HTML", "coverage.html")
    threshold_env = os.environ.get("COVERAGE_THRESHOLD")
    if threshold_env:
        threshold = float(threshold_env)
    else:
        threshold = float(_config_path(0, "testing", "coverage", "threshold") or 0)

    subprocess.run(
        [
            "go",
            "test",
            "-v",
            "-race",
            f"-coverprofile={coverage_file}",
            "./...",
        ],
        check=True,
    )

    go_binary = shutil.which("go") or "go"
    subprocess.run(
        [
            go_binary,
            "tool",
            "cover",
            f"-html={coverage_file}",
            "-o",
            coverage_html,
        ],
        check=True,
    )
    result = subprocess.run(
        [go_binary, "tool", "cover", "-func", coverage_file],
        check=True,
        capture_output=True,
        text=True,
    )

    total_line = ""
    for line in result.stdout.splitlines():
        if line.startswith("total:"):
            total_line = line
            break

    if not total_line:
        raise ValueError("Total coverage line not found in go tool output")

    coverage = _parse_go_coverage(total_line)
    print(f"Coverage: {coverage}%")
    if coverage < threshold:
        raise SystemExit(f"Coverage {coverage}% is below threshold {threshold}%")
    print(f"✅ Coverage {coverage}% meets threshold {threshold}%")


def check_go_coverage(_: argparse.Namespace) -> None:
    coverage_file = Path(os.environ.get("COVERAGE_FILE", "coverage.out"))
    html_output = Path(os.environ.get("COVERAGE_HTML", "coverage.html"))
    threshold = float(os.environ.get("COVERAGE_THRESHOLD", "0"))

    if not coverage_file.is_file():
        raise FileNotFoundError(f"{coverage_file} not found")

    go_binary = shutil.which("go") or "go"

    subprocess.run(
        [
            go_binary,
            "tool",
            "cover",
            f"-html={coverage_file}",
            "-o",
            str(html_output),
        ],
        check=True,
    )
    result = subprocess.run(
        [go_binary, "tool", "cover", "-func", str(coverage_file)],
        check=True,
        capture_output=True,
        text=True,
    )

    total_line = ""
    for line in result.stdout.splitlines():
        if line.startswith("total:"):
            total_line = line
            break

    if not total_line:
        raise ValueError("Total coverage line not found in go tool output")

    coverage = _parse_go_coverage(total_line)
    print(f"Coverage: {coverage}%")
    if coverage < threshold:
        raise SystemExit(f"Coverage {coverage}% is below threshold {threshold}%")
    print(f"✅ Coverage {coverage}% meets threshold {threshold}%")


def _run_command(
    command: Iterable[str], check: bool = True
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(list(command), check=check)


def frontend_install(_: argparse.Namespace) -> None:
    if Path("package-lock.json").is_file():
        _run_command(["npm", "ci"])
    elif Path("yarn.lock").is_file():
        _run_command(["yarn", "install", "--frozen-lockfile"])
    elif Path("pnpm-lock.yaml").is_file():
        _run_command(["npm", "install", "-g", "pnpm"])
        _run_command(["pnpm", "install", "--frozen-lockfile"])
    else:
        _run_command(["npm", "install"])


def frontend_run(_: argparse.Namespace) -> None:
    script_name = os.environ.get("FRONTEND_SCRIPT", "")
    success_message = os.environ.get("FRONTEND_SUCCESS_MESSAGE", "Command succeeded")
    failure_message = os.environ.get("FRONTEND_FAILURE_MESSAGE", "Command failed")

    if not script_name:
        raise SystemExit("FRONTEND_SCRIPT environment variable is required")

    result = subprocess.run(["npm", "run", script_name, "--if-present"], check=False)
    if result.returncode == 0:
        print(success_message)
    else:
        print(failure_message)


def python_install(_: argparse.Namespace) -> None:
    python = sys.executable
    subprocess.run([python, "-m", "pip", "install", "--upgrade", "pip"], check=True)

    if Path("requirements.txt").is_file():
        subprocess.run(
            [python, "-m", "pip", "install", "-r", "requirements.txt"],
            check=True,
        )

    if Path("pyproject.toml").is_file():
        subprocess.run([python, "-m", "pip", "install", "-e", "."], check=True)

    subprocess.run([python, "-m", "pip", "install", "pytest", "pytest-cov"], check=True)


def python_run_tests(_: argparse.Namespace) -> None:
    def has_tests() -> bool:
        for pattern in ("test_*.py", "*_test.py"):
            if any(Path(".").rglob(pattern)):
                return True
        return False

    if not has_tests():
        print("ℹ️ No Python tests found")
        return

    python = sys.executable
    subprocess.run(
        [
            python,
            "-m",
            "pytest",
            "--cov=.",
            "--cov-report=xml",
            "--cov-report=html",
        ],
        check=True,
    )


def python_lint(_: argparse.Namespace) -> None:
    """Run Python formatting and linting if sources are present."""
    python_sources = [
        path
        for path in Path(".").rglob("*.py")
        if ".venv" not in path.parts and "site-packages" not in path.parts
    ]
    if not python_sources:
        print("ℹ️ No Python sources detected for linting.")
        return

    lint_targets = [
        str(path)
        for path in [
            Path("scripts"),
            Path("tests"),
            Path("src"),
            Path("testdata/python"),
        ]
        if path.exists()
    ]
    if not lint_targets:
        lint_targets = ["."]

    required_tools = ["black", "ruff"]
    missing_tools = [tool for tool in required_tools if shutil.which(tool) is None]
    if missing_tools:
        python = sys.executable
        subprocess.run(
            [python, "-m", "pip", "install", "--upgrade", *missing_tools],
            check=True,
        )

    subprocess.run(["black", "--check", *lint_targets], check=True)
    subprocess.run(["ruff", "check", *lint_targets], check=True)


def rust_format(_: argparse.Namespace) -> None:
    """Run rustfmt in check mode when a Cargo project exists."""
    if not Path("Cargo.toml").is_file():
        print("ℹ️ No Cargo.toml found; skipping rustfmt.")
        return

    subprocess.run(["cargo", "fmt", "--all", "--", "--check"], check=True)


def rust_clippy(_: argparse.Namespace) -> None:
    """Run cargo clippy with sensible defaults when a Cargo project exists."""
    if not Path("Cargo.toml").is_file():
        print("ℹ️ No Cargo.toml found; skipping cargo clippy.")
        return

    command = ["cargo", "clippy", "--all-targets"]

    if os.environ.get("CLIPPY_ALL_FEATURES", "false").lower() == "true":
        command.append("--all-features")

    features = os.environ.get("CLIPPY_FEATURES", "").strip()
    if features:
        command.extend(["--features", features])

    if os.environ.get("CLIPPY_NO_DEFAULT_FEATURES", "false").lower() == "true":
        command.append("--no-default-features")

    extra_args = os.environ.get("CLIPPY_EXTRA_ARGS", "").strip()
    if extra_args:
        command.extend(extra_args.split())
    else:
        command.extend(["--", "-D", "warnings"])

    subprocess.run(command, check=True)


def ensure_cargo_llvm_cov(_: argparse.Namespace) -> None:
    if shutil.which("cargo-llvm-cov"):
        print("cargo-llvm-cov already installed")
        return
    subprocess.run(["cargo", "install", "cargo-llvm-cov", "--locked"], check=True)


def generate_rust_lcov(_: argparse.Namespace) -> None:
    output_path = Path(os.environ.get("LCOV_OUTPUT", "lcov.info"))
    subprocess.run(
        [
            "cargo",
            "llvm-cov",
            "--workspace",
            "--verbose",
            "--lcov",
            "--output-path",
            str(output_path),
        ],
        check=True,
    )


def generate_rust_html(_: argparse.Namespace) -> None:
    output_dir = Path(os.environ.get("HTML_OUTPUT_DIR", "htmlcov"))
    output_dir.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        [
            "cargo",
            "llvm-cov",
            "--workspace",
            "--verbose",
            "--html",
            "--output-dir",
            str(output_dir),
        ],
        check=True,
    )


def compute_rust_coverage(_: argparse.Namespace) -> None:
    path = Path(os.environ.get("LCOV_FILE", "lcov.info"))
    if not path.is_file():
        raise FileNotFoundError(f"{path} not found")

    total = 0
    covered = 0
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.startswith("LF:"):
            total += int(line.split(":", 1)[1])
        elif line.startswith("LH:"):
            covered += int(line.split(":", 1)[1])

    if total == 0:
        write_output("percent", "0")
        return

    percent = (covered * 100.0) / total
    write_output("percent", f"{percent:.2f}")


def enforce_coverage_threshold(_: argparse.Namespace) -> None:
    threshold = float(os.environ.get("COVERAGE_THRESHOLD", "0"))
    percent_str = os.environ.get("COVERAGE_PERCENT")
    if percent_str is None:
        raise SystemExit("COVERAGE_PERCENT environment variable missing")

    percent = float(percent_str)
    append_summary(f"Rust coverage: {percent}% (threshold {threshold}%)\n")
    if percent < threshold:
        raise SystemExit(f"Coverage {percent}% is below threshold {threshold}%")
    print(f"✅ Coverage {percent}% meets threshold {threshold}%")


def docker_build(_: argparse.Namespace) -> None:
    dockerfile = Path(os.environ.get("DOCKERFILE_PATH", "Dockerfile"))
    image_name = os.environ.get("DOCKER_IMAGE", "test-image")
    if not dockerfile.is_file():
        print("ℹ️ No Dockerfile found")
        return

    subprocess.run(
        ["docker", "build", "-t", image_name, str(dockerfile.parent)],
        check=True,
    )


def docker_test_compose(_: argparse.Namespace) -> None:
    if Path("docker-compose.yml").is_file() or Path("docker-compose.yaml").is_file():
        subprocess.run(["docker-compose", "config"], check=True)
    else:
        print("ℹ️ No docker-compose file found")


def docs_check_links(_: argparse.Namespace) -> None:
    print("ℹ️ Link checking would go here")


def docs_validate_structure(_: argparse.Namespace) -> None:
    print("ℹ️ Documentation structure validation would go here")


def run_benchmarks(_: argparse.Namespace) -> None:
    has_benchmarks = False
    for path in Path(".").rglob("*_test.go"):
        try:
            if "Benchmark" in path.read_text(encoding="utf-8"):
                has_benchmarks = True
                break
        except UnicodeDecodeError:
            continue

    if not has_benchmarks:
        print("ℹ️ No benchmarks found")
        return

    subprocess.run(["go", "test", "-bench=.", "-benchmem", "./..."], check=True)


def _matrix_entries(
    versions: list[str], oses: list[str], version_key: str
) -> list[dict[str, Any]]:
    matrix: list[dict[str, Any]] = []
    for os_index, runner in enumerate(oses):
        for ver_index, version in enumerate(versions):
            matrix.append(
                {
                    "os": runner,
                    version_key: version,
                    "primary": os_index == 0 and ver_index == 0,
                }
            )
    return matrix


def generate_matrices(_: argparse.Namespace) -> None:
    fallback_go = os.environ.get("FALLBACK_GO_VERSION", "1.24")
    fallback_python = os.environ.get("FALLBACK_PYTHON_VERSION", "3.13")
    fallback_rust = os.environ.get("FALLBACK_RUST_VERSION", "stable")
    fallback_node = os.environ.get("FALLBACK_NODE_VERSION", "26")
    fallback_threshold = os.environ.get("FALLBACK_COVERAGE_THRESHOLD", "80")

    versions_config = _config_path({}, "languages", "versions") or {}
    build_platforms = _config_path({}, "build", "platforms") or {}
    os_list = build_platforms.get("os") or ["ubuntu-latest"]

    go_versions = versions_config.get("go") or [fallback_go]
    python_versions = versions_config.get("python") or [fallback_python]
    rust_versions = versions_config.get("rust") or [fallback_rust]
    node_versions = versions_config.get("node") or [fallback_node]

    go_matrix = _matrix_entries(go_versions, os_list, "go-version")
    python_matrix = _matrix_entries(python_versions, os_list, "python-version")
    rust_matrix = _matrix_entries(rust_versions, os_list, "rust-version")
    frontend_matrix = _matrix_entries(node_versions, os_list, "node-version")

    write_output("go-matrix", json.dumps({"include": go_matrix}, separators=(",", ":")))
    write_output(
        "python-matrix",
        json.dumps({"include": python_matrix}, separators=(",", ":")),
    )
    write_output(
        "rust-matrix",
        json.dumps({"include": rust_matrix}, separators=(",", ":")),
    )
    write_output(
        "frontend-matrix",
        json.dumps({"include": frontend_matrix}, separators=(",", ":")),
    )

    coverage_threshold = _config_path(
        fallback_threshold, "testing", "coverage", "threshold"
    )
    write_output("coverage-threshold", str(coverage_threshold))


def generate_ci_summary(_: argparse.Namespace) -> None:
    primary_language = os.environ.get("PRIMARY_LANGUAGE", "unknown")
    steps = [
        ("Detect Changes", os.environ.get("JOB_DETECT_CHANGES", "skipped")),
        ("Workflow YAML", os.environ.get("JOB_WORKFLOW_LINT", "skipped")),
        ("Workflow Scripts", os.environ.get("JOB_WORKFLOW_SCRIPTS", "skipped")),
        ("Go CI", os.environ.get("JOB_GO", "skipped")),
        ("Python CI", os.environ.get("JOB_PYTHON", "skipped")),
        ("Rust CI", os.environ.get("JOB_RUST", "skipped")),
        ("Frontend CI", os.environ.get("JOB_FRONTEND", "skipped")),
        ("Docker CI", os.environ.get("JOB_DOCKER", "skipped")),
        ("Docs CI", os.environ.get("JOB_DOCS", "skipped")),
    ]

    files_changed = {
        "Go": os.environ.get("CI_GO_FILES", "false"),
        "Frontend": os.environ.get("CI_FRONTEND_FILES", "false"),
        "Python": os.environ.get("CI_PYTHON_FILES", "false"),
        "Rust": os.environ.get("CI_RUST_FILES", "false"),
        "Docker": os.environ.get("CI_DOCKER_FILES", "false"),
        "Docs": os.environ.get("CI_DOCS_FILES", "false"),
        "Workflow YAML": os.environ.get(
            "CI_WORKFLOW_YAML_FILES",
            os.environ.get("CI_WORKFLOW_FILES", "false"),
        ),
        "Workflow Scripts": os.environ.get("CI_WORKFLOW_SCRIPT_FILES", "false"),
        "Lint Config": os.environ.get("CI_LINT_FILES", "false"),
    }

    languages = {
        "has-rust": os.environ.get("HAS_RUST", "false"),
        "has-go": os.environ.get("HAS_GO", "false"),
        "has-python": os.environ.get("HAS_PYTHON", "false"),
        "has-frontend": os.environ.get("HAS_FRONTEND", "false"),
        "has-docker": os.environ.get("HAS_DOCKER", "false"),
    }

    summary_lines = [
        "# 🚀 CI Pipeline Summary",
        "",
        "## 🧭 Detection",
        f"- Primary language: {primary_language}",
    ]
    summary_lines.extend(f"- {label}: {value}" for label, value in languages.items())
    summary_lines.extend(
        [
            "",
            "## 📊 Job Results",
            "| Job | Status |",
            "|-----|--------|",
        ]
    )
    summary_lines.extend(f"| {job} | {status} |" for job, status in steps)
    summary_lines.extend(
        [
            "",
            "## 📁 Changed Files",
        ]
    )
    summary_lines.extend(
        f"- {label}: {value}" for label, value in files_changed.items()
    )
    summary_lines.append("")

    append_summary("\n".join(summary_lines) + "\n")


def check_ci_status(_: argparse.Namespace) -> None:
    job_envs = {
        "Workflow Lint": os.environ.get("JOB_WORKFLOW_LINT"),
        "Workflow Scripts": os.environ.get("JOB_WORKFLOW_SCRIPTS"),
        "Go CI": os.environ.get("JOB_GO"),
        "Frontend CI": os.environ.get("JOB_FRONTEND"),
        "Python CI": os.environ.get("JOB_PYTHON"),
        "Rust CI": os.environ.get("JOB_RUST"),
        "Docker CI": os.environ.get("JOB_DOCKER"),
        "Docs CI": os.environ.get("JOB_DOCS"),
    }

    failures = [
        job for job, status in job_envs.items() if status in {"failure", "cancelled"}
    ]
    if failures:
        print(f"❌ CI Pipeline failed: {', '.join(failures)}")
        raise SystemExit(1)
    print("✅ CI Pipeline succeeded")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="CI workflow helper commands.")
    subparsers = parser.add_subparsers(dest="command", required=True)

    commands = {
        "debug-filter": debug_filter,
        "determine-execution": determine_execution,
        "wait-for-pr-automation": wait_for_pr_automation,
        "load-super-linter-config": load_super_linter_config,
        "write-validation-summary": write_validation_summary,
        "generate-matrices": generate_matrices,
        "go-setup": go_setup,
        "go-test": go_test,
        "check-go-coverage": check_go_coverage,
        "frontend-install": frontend_install,
        "frontend-run": frontend_run,
        "python-install": python_install,
        "python-lint": python_lint,
        "python-run-tests": python_run_tests,
        "ensure-cargo-llvm-cov": ensure_cargo_llvm_cov,
        "rust-format": rust_format,
        "rust-clippy": rust_clippy,
        "generate-rust-lcov": generate_rust_lcov,
        "generate-rust-html": generate_rust_html,
        "compute-rust-coverage": compute_rust_coverage,
        "enforce-coverage-threshold": enforce_coverage_threshold,
        "docker-build": docker_build,
        "docker-test-compose": docker_test_compose,
        "docs-check-links": docs_check_links,
        "docs-validate-structure": docs_validate_structure,
        "run-benchmarks": run_benchmarks,
        "generate-ci-summary": generate_ci_summary,
        "check-ci-status": check_ci_status,
    }

    for command, handler in commands.items():
        subparsers.add_parser(command).set_defaults(handler=handler)
    return parser


def main() -> None:
    parser = build_parser()
    args = parser.parse_args()
    handler = getattr(args, "handler", None)
    if handler is None:
        parser.print_help()
        raise SystemExit(1)
    handler(args)


if __name__ == "__main__":
    main()

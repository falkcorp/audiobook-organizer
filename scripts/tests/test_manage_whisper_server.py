# file: scripts/tests/test_manage_whisper_server.py
# version: 1.1.0
# guid: 5b0c7a52-3d0e-4c1c-9a6e-0e0a4f3b7d21
# last-edited: 2026-11-17

"""The PowerShell that manage-whisper-server.py --restart sends over WinRM.

WinRM runs Windows PowerShell 5.1, so the script must not use PowerShell 7 syntax (a
`?.` there is a parse error and --restart would kill the server and never start it),
and a path with a space must reach the server as one argument. These tests build the
generated scripts and PARSE them with the real PowerShell parser: powershell.exe on
Windows (the CI job), pwsh elsewhere, skipped when neither exists."""

import importlib.util
import os
import pathlib
import shutil
import subprocess
import sys
import types
import unittest

HERE = pathlib.Path(__file__).resolve().parent

PARSE = (
    "$errs = $null; "
    "[void][System.Management.Automation.Language.Parser]::ParseInput("
    "$env:PS_UNDER_TEST, [ref]$null, [ref]$errs); "
    "if ($errs.Count) { $errs | ForEach-Object { $_.Message }; exit 1 }"
)


def _load():
    fake = types.ModuleType("winrm")
    fake.Session = object  # only used in annotations
    sys.modules.setdefault("winrm", fake)
    os.environ.setdefault("ABK_GPU_HOST", "arkham.example")
    spec = importlib.util.spec_from_file_location(
        "manage_whisper_server", HERE.parent / "manage-whisper-server.py"
    )
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _ps_exe():
    if sys.platform == "win32":
        return shutil.which("powershell.exe") or shutil.which("powershell")
    return shutil.which("pwsh")


class ManageWhisperServerScripts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mws = _load()

    def scripts(self):
        mws = self.mws
        return {
            "kill": mws.build_kill_script(19847),
            "launch_default": mws.build_launch_script(
                r"C:\Users\Bruce", "auto", "", "127.0.0.1", 19847
            ),
            "launch_space": mws.build_launch_script(
                r"C:\Users\Bruce Wayne", "cpu", "base.en", "0.0.0.0", 19850
            ),
            "launch_quote": mws.build_launch_script(
                r"C:\Users\O'Hara", "cuda", "large-v2", "::1", 1
            ),
        }

    def test_the_generated_script_parses_in_powershell(self):
        exe = _ps_exe()
        if not exe:
            self.skipTest("no PowerShell on this machine")
        for name, text in self.scripts().items():
            with self.subTest(script=name):
                out = subprocess.run(
                    [exe, "-NoProfile", "-Command", PARSE],
                    capture_output=True,
                    text=True,
                    env={**os.environ, "PS_UNDER_TEST": text},
                    timeout=120,
                )
                self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    def test_no_powershell_7_only_syntax(self):
        # pwsh parses these happily, so the parse test cannot see them on a Mac: check the
        # text. Windows PowerShell 5.1 has none of: ?. ?[] ?? ??= && || ternary.
        for name, text in self.scripts().items():
            for token in ("?.", "?[", "??", "&&", "||"):
                self.assertNotIn(token, text, f"{name}: PowerShell 7 syntax {token!r}")
            self.assertNotIn(" ? ", text, f"{name}: ternary")

    def test_a_path_with_a_space_is_one_argument(self):
        script = self.scripts()["launch_space"]
        self.assertIn("'\"C:\\Users\\Bruce Wayne\\whisper_server.py\"'", script)
        # Start-Process does not quote elements, so the embedded double quotes ARE the
        # quoting; a bare 'C:\Users\Bruce Wayne\...' element would split into two.
        self.assertNotIn("'C:\\Users\\Bruce Wayne", script)

    def test_a_single_quote_in_a_value_cannot_break_out_of_its_literal(self):
        script = self.scripts()["launch_quote"]
        self.assertIn("O''Hara", script)
        self.assertNotIn("O'Hara", script.replace("O''Hara", ""))

    def test_a_value_with_a_space_that_ends_in_a_backslash_keeps_its_closing_quote(self):
        # `C:\Models\Wayne Manor\` ends in a backslash, which in front of our closing
        # double quote would escape it and swallow the next argument. The Windows parser's
        # own rule is what subprocess.list2cmdline implements, on any platform: the element
        # must be exactly what it would produce.
        for value in (
            "C:\\Models\\Wayne Manor\\",
            "C:\\Models\\Wayne Manor\\\\",
            'say "Gotham" now',
            'trailing quote and space "',
            'C:\\a b\\\\"c',
            "plain",
        ):
            with self.subTest(value=value):
                literal = self.mws.start_process_arg(value)
                self.assertTrue(literal.startswith("'") and literal.endswith("'"))
                inner = literal[1:-1].replace("''", "'")
                if any(c.isspace() or c == '"' for c in value):
                    self.assertEqual(inner, subprocess.list2cmdline([value]))
                else:
                    self.assertEqual(inner, value)
        self.assertEqual(
            self.mws.start_process_arg("C:\\Models\\Wayne Manor\\"),
            "'\"C:\\Models\\Wayne Manor\\\\\"'",
        )

    def test_the_launch_arguments_carry_every_setting(self):
        s = self.scripts()["launch_space"]
        for piece in (
            "'--device', 'cpu'",
            "'--model', 'base.en'",
            "'--host', '0.0.0.0'",
            "'--port', '19850'",
        ):
            self.assertIn(piece, s)
        self.assertNotIn("--model", self.scripts()["launch_default"], "no model flag unless asked")

    def test_the_kill_script_targets_only_the_listening_port(self):
        kill = self.mws.build_kill_script(19847)
        self.assertIn(":19847", kill)
        self.assertIn("LISTENING", kill)
        with self.assertRaises(ValueError):
            self.mws.build_kill_script("19847; Remove-Item C:\\")


if __name__ == "__main__":
    unittest.main()

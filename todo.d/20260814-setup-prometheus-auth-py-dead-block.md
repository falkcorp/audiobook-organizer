- [ ] **Check `scripts/setup-prometheus-auth.py` for the dead-indentation
      bug found in its server-side shell sibling.** The staged
      `abo-prometheus-auth.sh` (server home dir, patched in place to v1.0.1
      on 2026-08-14) computed a YAML body indent from a whitespace-only
      regex capture and then called `.index('-')` on it — a guaranteed
      `ValueError` for any list-style `- job_name:` entry, i.e. every real
      prometheus.yml. If the repo script shares the pattern, fix it there
      too; if not, note that the shell script diverged.

#!/usr/bin/env python3
# file: docs/plans/platform-core/tools/check_waves.py
# version: 1.0.0
# guid: 70c618d9-ad53-44e1-a46a-c1b8f03fe864
# last-edited: 2026-11-05
"""Mechanical checks over inventory.json.

Usage: python3 -I check_waves.py <inventory.json> <spec dir (docs/specs/platform-core)>

Checks:
  1. schema: every row has id, title, release, wave, tier, depends_on, files, spec, notes; ID scheme.
  2. dependencies: every depends_on exists and sits in an earlier wave.
  3. disjointness: inside each wave, no two tasks share a file; a path ending in '/' is a
     directory and collides with every path below it.
  4. 04 coverage: every core package of 04 §8.2, every (source, destination, stage) source of 04 §8.2,
     every non-ABO row of 04 §11, every new path or rename of 04 §2.4 and every core package 04 §8.1
     creates is covered by exactly one task's '04 keys' (exclusions listed with their reason).
  5. gates: every gate test is owned by exactly one task, and every test name 09 cites is in a gate.
  6. real paths: no placeholder token; a path that is a directory at the base commit ends in '/', a file does not;
     no row lists a path that a row of an earlier wave removed (a whole-directory move, rename or deletion), and
     no row lists a rename's destination before the rename's wave.
Prints PASS/FAIL per check and exits 1 on any failure.
"""
import json
import re
import sys
from collections import Counter, defaultdict

inv_path, spec_dir = sys.argv[1], sys.argv[2]
inv = json.load(open(inv_path))
fails = []


def fail(msg):
    fails.append(msg)


# 1. schema
FIELDS = ["id", "title", "release", "wave", "tier", "depends_on", "files", "spec", "notes"]
ID_RE = re.compile(r"^(R[0-3]|F[2-9])-\d{2}$|^OWNER-\d{2}$")
for r in inv:
    if list(r.keys()) != FIELDS:
        fail("schema %s: %s" % (r.get("id"), list(r.keys())))
    if not ID_RE.match(r["id"]):
        fail("id scheme: " + r["id"])
    if r["id"].startswith("OWNER"):
        if r["files"] or r["tier"] != "OWNER" or "NOT AGENT WORK" not in r["notes"]:
            fail("owner row shape: " + r["id"])
    else:
        if not r["files"]:
            fail("agent task without files: " + r["id"])
        if r["tier"] not in ("T0", "T1", "T2", "T3", "T4"):
            fail("tier: " + r["id"])
        if not r["id"].startswith(r["release"] + "-"):
            fail("id does not encode its release: %s / %s" % (r["id"], r["release"]))
ids = [r["id"] for r in inv]
if len(ids) != len(set(ids)):
    fail("duplicate ids")
print("1 schema and ID scheme:", "PASS" if not fails else "FAIL")
n_before = len(fails)

# 2. dependencies
by = {r["id"]: r for r in inv}
wnum = {r["id"]: int(r["wave"][1:]) for r in inv}
for r in inv:
    for d in r["depends_on"]:
        if d not in by:
            fail("unknown dependency %s -> %s" % (r["id"], d))
        elif wnum[d] >= wnum[r["id"]]:
            fail("dependency not in an earlier wave: %s (%s) -> %s (%s)" % (r["id"], r["wave"], d, by[d]["wave"]))
print("2 dependencies in earlier waves:", "PASS" if len(fails) == n_before else "FAIL")
n_before = len(fails)


# 3. disjointness
def collide(a, b):
    return a == b or (a.endswith("/") and b.startswith(a)) or (b.endswith("/") and a.startswith(b))


waves = defaultdict(list)
for r in inv:
    waves[r["wave"]].append(r)
pairs = 0
for w, rs in sorted(waves.items()):
    for i in range(len(rs)):
        for j in range(i + 1, len(rs)):
            pairs += 1
            for a in rs[i]["files"]:
                for b in rs[j]["files"]:
                    if collide(a, b):
                        fail("wave %s: %s and %s share %s / %s" % (w, rs[i]["id"], rs[j]["id"], a, b))
print("3 file-set disjointness inside each of %d waves (%d task pairs):" % (len(waves), pairs),
      "PASS" if len(fails) == n_before else "FAIL")
n_before = len(fails)

# 4. 04 coverage
cover = defaultdict(list)
for r in inv:
    m = re.search(r"04 keys: (.*?)\.(?: Owns gate tests:| Flip PR:|$)", r["notes"])
    if m:
        for k in m.group(1).split("; "):
            cover[k].append(r["id"])


def norm(k):
    return re.sub(r" \([^)]*\)", "", k).strip()


ncover = defaultdict(list)
for k, v in cover.items():
    ncover[norm(k)].extend(v)
for k, v in ncover.items():
    if len(v) != 1:
        fail("04 key covered %d times: %s %s" % (len(v), k, v))

t04 = open(spec_dir + "/04-package-mapping.md").read().split("\n")


def section(start, end):
    s = [i for i, l in enumerate(t04) if l.startswith(start)][0]
    e = [i for i, l in enumerate(t04) if l.startswith(end) and i > s][0]
    return t04[s:e]


expected = set()
# 04 §8.2: core packages with their first stage, and every source with its stage
for l in section("### 8.2", "### 8.3"):
    if l.startswith("| ") and not l.startswith("| first stage") and not l.startswith("|---"):
        c = [x.strip() for x in l.strip("|").split("|")]
        stage, pkg = c[0], c[1].strip("`")
        expected.add("%s create %s" % (stage, pkg))
        for m in re.finditer(r"`([^`]+)`(?: \(([RF][0-9])\b[^)]*\))?", c[2]):
            expected.add("%s move %s -> %s" % (m.group(2) or stage, m.group(1), pkg))
# 04 §2.4: new root-module paths and renames
for l in section("### 2.4", "## 3."):
    m = re.match(r"\| `([^`]+)`(?: \(\+[^)]*\))?(?: \(name open\))? \| (R1|F\d) ?(\(rename\))? \|", l)
    if m:
        path, stage, ren = m.group(1), m.group(2), m.group(3)
        if ren:
            src = {"internal/books/store": "internal/database", "internal/books/writer": "internal/operations/opswriter",
                   "internal/telemetrypolicy": "internal/telemetry/contract"}[path]
            expected.add("%s rename %s -> %s" % (stage, src, path))
        else:
            expected.add("%s create %s" % (stage, path))
# 04 §8.1: core packages with no HEAD source (they appear only in the §8.1 prose); the list is read from
# 04 §8.1's 'core packages that first exist' column and checked against 03 §3.3's tree by review.
CORE_81 = {
    "R1": ["core module", "mediacore/buildid", "mediacore/storage/keys", "mediacore/entity", "mediacore/version",
           "mediacore/module", "mediacore/app", "mediacore/examples/hello", "mediacore/internal/httpmw",
           "mediacore/internal/openapi", "mediacore/internal/jsonschemax", "mediacore/rbac", "mediacore/auth",
           "mediacore/ui", "mediacore/storage/restorejournal", "mediacore/internal/restoreswap",
           "mediacore/coretest/crashmatrix", "npm @mediacore/api-client", "core/openapi/core.json"],
    "F2": ["mediacore/storage/datamigrate", "mediacore/internal/storagemigrate", "mediacore/health/diagnostics"],
    "F3": ["mediacore/config/configtest", "mediacore/secrets", "mediacore/internal/keyedmutex"],
    "F4": ["mediacore/rbac/rbactest", "mediacore/auth/authtest", "mediacore/auth/password", "mediacore/backup/journal",
           "mediacore/events", "mediacore/internal/uihttp", "mediacore/internal/modulehttp", "mediacore/internal/storagehttp"],
    "F5": ["mediacore/internal/logjournal", "mediacore/log/logtest"],
    "F6": ["mediacore/outbox", "mediacore/internal/sse", "mediacore/internal/eventhttp"],
    "F7": ["mediacore/integrations", "mediacore/integrations/integrationstest", "mediacore/integrations/ai/aitest",
           "mediacore/integrations/metadata", "mediacore/integrations/download", "mediacore/integrations/notify",
           "mediacore/integrations/notify/notifytest", "mediacore/integrations/providerhttp/providerhttptest"],
    "F9": ["npm @mediacore/ui-kit", "npm @mediacore/shell", "hello web package"],
}
for st, ps in CORE_81.items():
    for p in ps:
        expected.add("%s create %s" % (st, p))
# 04 §11 rows whose path leaves the root module (core-public, core-internal, merge, delete)
EXCLUDED = {"internal/database/mocks":
            "delete with the optional mock conversion: 'none; R1-R2 as the owner decides' (09 §9.4.1 last row; 04 §4.6, open item 1)"}
moved_srcs = {norm(k).split(" ")[2] for k in ncover if " move " in k or " merge " in k}
for l in t04[[i for i, l in enumerate(t04) if l.startswith("## 11.")][0]:]:
    m = re.match(r"\| \[`([^`]+)`\]\(#[^)]+\) \| ([a-z-]+) \| (.*) \|$", l)
    if not m or m.group(2) == "abo":
        continue
    pkg, bucket = m.group(1), m.group(2)
    if pkg in EXCLUDED:
        continue
    if bucket == "delete":
        if not any(norm(k).endswith(" delete " + pkg) for k in ncover):
            fail("04 §11 delete row not covered: " + pkg)
    elif pkg not in moved_srcs:
        fail("04 §11 %s row not covered by a move or merge: %s" % (bucket, pkg))
missing = sorted(e for e in expected if e not in ncover)
for e in missing:
    fail("04 key not covered: " + e)
extra = sorted(k for k in ncover if k not in expected)
print("4 04 coverage (%d expected keys, %d covered keys, %d plan-added keys, %d excluded):" % (
    len(expected), len(ncover), len(extra), len(EXCLUDED)), "PASS" if len(fails) == n_before else "FAIL")
for k in extra:
    print("   plan-added key (a grow, delete, merge, rename or file-level key beyond 04 §8.2):", k, ncover[k])
for k, why in EXCLUDED.items():
    print("   excluded:", k, "--", why)
n_before = len(fails)

# 5. gates
owned = defaultdict(list)
for r in inv:
    m = re.search(r"Owns gate tests: (.*?)\.(?: Flip PR:|$)", r["notes"])
    if m:
        for g in m.group(1).split("; "):
            owned[g].append(r["id"])
for g, v in owned.items():
    if len(v) != 1:
        fail("gate owned %d times: %s %s" % (len(v), g, v))
t09 = open(spec_dir + "/09-abo-on-core.md").read()
body09 = t09.split("## Review history")[0]
cited = set(re.findall(r"`(Test[A-Za-z0-9_]+)`", body09))
alltext = " ".join(owned)
for t in sorted(cited):
    if not re.search(r"\b%s\b" % re.escape(t), alltext):
        fail("test cited by 09 but owned by no task: " + t)
for s in sorted(set(re.findall(r"`(scripts/(?:tests|ci)/[a-z_]+\.(?:py|sh))`", body09))):
    if s not in alltext:
        fail("script check cited by 09 but owned by no task: " + s)
print("5 gate ownership (%d gates, %d test names cited by 09):" % (len(owned), len(cited)),
      "PASS" if len(fails) == n_before else "FAIL")

n_before = len(fails)

# 6. real paths
import os
import subprocess
repo = os.path.normpath(os.path.join(spec_dir, "..", "..", ".."))
tracked = set(subprocess.run(["git", "-C", repo, "ls-files"], capture_output=True, text=True, check=True).stdout.split("\n"))
tracked.discard("")
tdirs = set()
for f in tracked:
    parts = f.split("/")
    for k in range(1, len(parts)):
        tdirs.add("/".join(parts[:k]) + "/")
# whole directories a row removes (moves away, renames or deletes), with the removing row
GONE = {
    "internal/lifecycle/": "R1-07", "internal/logging/": "R1-07", "internal/security/safepath/": "R1-08",
    "internal/security/pathvalidation/": "R1-08", "internal/libroot/": "R1-08", "internal/boundedcall/": "R1-08",
    "internal/diskstats/": "R1-08", "internal/diag/flight/": "R1-09", "internal/telemetry/contract/": "R1-10",
    "internal/sdnotify/": "R1-16", "internal/compactprogress/": "R1-20", "internal/database/": "R1-26",
    "tools/cmd/oplint/": "F2-17", "internal/oauth/": "F4-03", "internal/operations/registry/": "F5-18",
    "internal/operations/childop/": "F5-18", "internal/operations/opmode/": "F5-18", "internal/operations/freshness/": "F5-18",
    "internal/operations/opswriter/": "F5-18", "internal/opscatalog/": "F5-18", "internal/opsmetrics/": "F5-18",
    "internal/walkresume/": "F5-18", "internal/scheduler/": "F5-18", "pkg/ops/": "F5-18", "internal/realtime/": "F6-03",
    "internal/security/safehttp/": "F7-02", "internal/mail/": "F7-05", "internal/plugin/": "F7-09",
    "internal/aidispatch/": "F7-09", "internal/ai/aijobs/": "F7-09", "internal/deluge/": "F7-09", "internal/watcher/": "F7-09",
    "internal/plugins/webhook/": "F7-09", "internal/metadata/providerhttp/": "F7-09", "internal/updater/": "F8-04",
    "internal/backup/": "F8-04", "internal/sysinfo/": "F8-04", "internal/startup/": "F8-04",
    "internal/serviceregistry/": "R3-01", "pkg/plugin/sdk/": "R3-01", "tools/cmd/sdkguard/": "R3-01",
    "internal/bookfiles/": "R3-01", "internal/writeback/": "R3-01", "internal/models/": "R3-01",
    "internal/books/cutover/": "R3-02", "internal/operations/state/": "R3-02", "tools/cmd/opsgen/": "R3-02",
}
# paths that exist only from a row's wave on (a rename's destination or a package a row creates)
BORN = {"internal/books/store/": "R1-26", "internal/telemetrypolicy/": "R1-10", "internal/books/writer/": "F5-18",
        "internal/books/cutover/": "F3-06", "internal/cfaccess/": "F4-03", "internal/books/": "R1-15", "core/": "R1-01"}
n_exist = n_dir = n_new = 0
for r in inv:
    w = wnum[r["id"]]
    for t in r["files"]:
        if "<" in t or ">" in t:
            fail("placeholder path in %s: %s" % (r["id"], t))
            continue
        if t.endswith("/"):
            if t[:-1] in tracked:
                fail("%s lists file %s as a directory" % (r["id"], t))
            elif t in tdirs:
                n_dir += 1
            else:
                n_new += 1
        else:
            if t + "/" in tdirs:
                fail("%s lists directory %s without its trailing /" % (r["id"], t))
            elif t in tracked:
                n_exist += 1
            else:
                n_new += 1
        for p, who in GONE.items():
            if who != r["id"] and w > wnum[who] and (t == p or t.startswith(p)):
                fail("%s (%s) lists %s, removed by %s in %s" % (r["id"], r["wave"], t, who, by[who]["wave"]))
        for p, who in BORN.items():
            if who != r["id"] and w < wnum[who] and (t == p or t.startswith(p)):
                fail("%s (%s) lists %s before %s creates it in %s" % (r["id"], r["wave"], t, who, by[who]["wave"]))
print("6 real paths (%d tracked files, %d tracked directories, %d new paths):" % (n_exist, n_dir, n_new),
      "PASS" if len(fails) == n_before else "FAIL")

for f in fails:
    print("FAIL:", f)
print("RESULT:", "PASS" if not fails else "FAIL (%d)" % len(fails))
sys.exit(1 if fails else 0)

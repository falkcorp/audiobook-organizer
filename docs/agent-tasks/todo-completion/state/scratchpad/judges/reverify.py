import json, subprocess, sys, re, os
REPO = "/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer"
SK = "/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/dryrun/docs/agent-tasks/todo-completion/skeleton.json"
d = json.load(open(SK))
bucket = sys.argv[1]
items = d['buckets'][bucket] if bucket in d['buckets'] else d['tasks']
SAFE = re.compile(r'^\s*(grep|ggrep|rg|sed|awk|ls|wc|find|git grep|cat|head|tail|python3|jq|test)\b')
out = []
for e in items:
    tid = e.get('id') or ('L%s' % e['todo_line'])
    res = []
    for a in e.get('verified_anchors', []) or []:
        cmd = a['grep_cmd']
        if not SAFE.match(cmd):
            res.append({'cmd': cmd, 'status': 'SKIPPED_UNSAFE', 'expect': a.get('expect')})
            continue
        try:
            p = subprocess.run(['bash', '-c', cmd], cwd=REPO, capture_output=True, text=True, timeout=90)
            o = p.stdout.strip()
            n = len([x for x in o.split('\n') if x.strip()]) if o else 0
            res.append({'cmd': cmd, 'rc': p.returncode, 'nlines': n,
                        'out': o[:400], 'err': p.stderr.strip()[:200], 'expect': a.get('expect'),
                        'claim': a.get('claim', '')[:160]})
        except Exception as ex:
            res.append({'cmd': cmd, 'status': 'ERR:%s' % ex, 'expect': a.get('expect')})
    out.append({'id': tid, 'todo_line': e['todo_line'], 'title': e['title'][:150], 'anchors': res})
json.dump(out, open('/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/judges/reverify-%s.json' % bucket, 'w'), indent=1)
# summary
zero = [o for o in out if any(a.get('nlines') == 0 for a in o['anchors'])]
noanch = [o for o in out if not o['anchors']]
print(bucket, 'items', len(out), 'with a ZERO-HIT anchor:', len(zero), 'with NO anchors:', len(noanch))
for o in zero:
    print(' ZERO', o['id'], 'L%s' % o['todo_line'], o['title'][:90])
    for a in o['anchors']:
        if a.get('nlines') == 0:
            print('    cmd:', a['cmd'][:180])
            print('    expect:', str(a.get('expect'))[:160])
for o in noanch:
    print(' NOANCHOR', o['id'], 'L%s' % o['todo_line'], o['title'][:90])

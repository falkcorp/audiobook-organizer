import subprocess,re
todo=re.sub(r'\s+',' ',open('TODO.md').read().lower())
cases=[("c9d78ff1","todo.d/2026-08-19-bare-pebblestore-assertions-audit.md"),("c9d78ff1","todo.d/2026-08-20-bench-build-and-sdkguard-broken.md"),("c9d78ff1","todo.d/20260820-gofmt-sweep.md"),
("6658d1a8","todo.d/20260809-authors-page-aliases-crash.md"),("6658d1a8","todo.d/20260809-edit-dialog-blank-year-isbn.md"),("6658d1a8","todo.d/20260809-library-double-fetch-swallows-clicks.md"),("6658d1a8","todo.d/20260809-mui-select-menu-does-not-close-on-linux.md"),("6658d1a8","todo.d/20260809-three-linux-only-e2e-failures-block-ci-gate.md"),("6658d1a8","todo.d/20260809-webkit-scan-import-drawer-backdrop.md"),("6658d1a8","todo.d/20260810-search-index-queue-drops-silently.md"),
("f95c19c1","todo.d/20260808_225500_tag_cloud_collapsed_by_default.md"),("f5736934","todo.d/20260807_210800_e2e_content_drift_refresh.md"),("3024fb5c","todo.d/20260805_220200_series_names_that_are_book_numbers.md")]
for c,p in cases:
    body=subprocess.run(['git','show',f'{c}^:{p}'],capture_output=True,text=True).stdout
    lines=[re.sub(r'\s+',' ',l.strip().lower()) for l in body.splitlines() if len(l.strip())>40 and not l.startswith('<!--')]
    hits=sum(1 for l in lines[:8] if l[:50] in todo)
    print(f"{hits}/{min(8,len(lines))}  {p}  ::  {lines[0][:80] if lines else ''}")

// file: core/go.mod
// version: 1.0.0
// guid: 423dec24-3ee4-45a5-a8ca-af9d6bc2b133
// last-edited: 2026-11-05

module github.com/falkcorp/mediacore

go 1.27.0

// The one genproto exclude of the tree (03 §3.2 item 1). cockroachdb/errors
// v1.14.0 still requires the monolithic genproto, which workspace mode loads
// beside the split googleapis/{api,rpc} modules ("ambiguous import"). A
// workspace applies only its members' excludes, so it lives here and in no
// other go.mod. make -C core exclude-check and the root genproto-exclude-root
// job prove it present, green with it, and load-bearing without it.
exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1

// npm packages ship .go files; never scan them as core packages.
ignore ./web

ignore ./examples/hello/web

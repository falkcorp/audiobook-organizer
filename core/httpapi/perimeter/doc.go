// file: core/httpapi/perimeter/doc.go
// version: 1.0.0
// guid: bc23c03a-483d-432a-8feb-3cde74897213
// last-edited: 2026-11-08

// Package perimeter holds the black-box tests of core's HTTP perimeter: the
// fixed stage chain of 05-http-api §4.2 that httpapi.Router.Build puts
// around every request (stages 0-7 and 9 around the router, 10-11 inside
// each route's wrapper; stage 8, the Basic gate, stays in the legacy gin
// engine until the config flip F3). The stages are implemented in
// core/internal/httpmw and composed by httpapi, so this package exports
// nothing: the chain has no public name beyond httpapi's Options, Deps,
// ClientAddrFrom and RequestID (05-http-api §3).
package perimeter

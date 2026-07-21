### Security

#### Force `brace-expansion` >= 5.0.7 in `web/` via npm override

The frontend pulled in a vulnerable transitive `brace-expansion@5.0.6` (ReDoS). Added a
`brace-expansion` npm override in `web/package.json`, bumping it to 5.0.7. `npm` reports 0
vulnerabilities.

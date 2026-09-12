# Diff-scoped documentation follow-up — 2026-09-12

## Scope and result

The supplied CodeRabbit warning reports 26.19% docstring coverage against an 80%
threshold, with 42 analyzed and 33 skipped files. The local comparison uses
`9ce308d..e4836db`: the merge-base of `origin/main` and `dev` through the revision
reviewed here. Its 75 changed files match that file total. The remote checker was
not rerun, so its exact function selection and resulting percentage remain
unverified; the local counts below are not a reproduction of CodeRabbit's score.

Added the 29 missing Go doc comments found by comparing function bodies in that
range: 19 handler/helper methods and 10 test scenarios. All 115 changed Go
function/method declarations now have attached doc comments. All 16 changed named
JavaScript/TypeScript functions also have JSDoc. The changed top-level Playwright
test scenarios and relevant initialization/event handlers are documented too.
Tiny nested projection/filter callbacks are not included in the named-function
coverage figure.

Comments describe actual constraints: basis/invoice locks, account and passkey
ownership, focus restoration, stale search results, test isolation, and native
file-input behavior. The login backdrop's obsolete continuous-animation
description now matches its existing static rendering. No coverage threshold,
review exclusion, dependency, application behavior, or test assertion changed.

## Verification

- Go token comparison against `e4836db`: no executable changes in modified files.
- TypeScript-parser/printer token comparison for all modified JS/TS files against
  `e4836db`: no executable changes.
- `gofmt -l` on modified Go files and `git diff --check`: clean.
- `node --check` on all four modified runtime scripts: passed.
- Playwright `test --list --reporter=list`: all 32 tests loaded successfully.
  This checks discovery/transpilation, not browser execution. The initial listing
  hit a sandbox write restriction in the HTML reporter; the console-only rerun
  passed without changing permissions or the test configuration.
- `go build ./...` and `go vet -tags=integration ./...`: passed with Go 1.27.1.
- `go test -race -count=1 -shuffle=on ./...`: all 15 packages passed in an isolated
  container without a database connection. Environment-gated tests are skipped
  in this unit-only run.

The previous [full functional validation](2026-09-12-functional-regression.md)
records the PostgreSQL 16/18 and 32-test browser results. Those write-based suites
were not repeated for this comment-only change; their results are not presented
as new executions. No application deployment, database operation, push, or branch
synchronization was performed. CodeRabbit must recheck the PR after user sync.

The read-only audit helpers and per-function inventories remain machine-local
under the gitignored `tmp/_docstrings-20260912/` directory. CodeRabbit's documented
language-aware approach is described in its
[docstring guide](https://docs.coderabbit.ai/finishing-touches/docstrings).

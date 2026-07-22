# Nestorage

Household storage management: the items, the bins that hold them, and the
locations those bins live in. Nestorage is a local-first LAN appliance, not a
cloud app — it runs on always-on hardware in the house and does not depend on
internet reachability. It consumes the shared [nestcore](https://github.com/ericfisherdev/nestcore)
platform module for configuration, database access, HTTP serving, and the
other cross-cutting concerns Nestorage shares with its sibling app, Nestova.

## Repository layout

This repo uses a bare clone plus worktrees, never a plain clone:

```
nestorage/
  .bare/     the bare clone
  .git       a file containing "gitdir: ./.bare"
  main/      the main worktree
  NSTR-2/    per-ticket worktrees
```

`.bare/` holds the actual repository data. `.git` at the repo root is a plain
text file (not a directory) pointing at it. `main/` is the worktree for the
default branch, and every ticket branch gets its own sibling worktree so
multiple branches can be checked out and built at once without stashing.

Add a worktree for a new ticket with:

```sh
git -C /home/esfisher/dev/housedev/nestorage worktree add NSTR-2 -b feature/NSTR-2-scaffold
```

## Development

### Prerequisites

- Go (see the `go` directive in [`go.mod`](go.mod))
- [golangci-lint](https://golangci-lint.run) **v2.11.4**, installed as a
  pinned release binary rather than via `go install` (the project's own
  recommendation — it risks building against an untested Go version or
  dependency set). This matches `GOLANGCI_LINT_VERSION` in the
  [`Makefile`](Makefile) and the version pinned in the lint job of
  [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

Everything else — lefthook, conform — is pinned in `go.mod` via Go tool
directives, so no global install is needed: invoke it with `go tool <name>`.

### The local dev loop

```sh
git -C /home/esfisher/dev/housedev/nestorage worktree add <dir> -b <branch>
cd /home/esfisher/dev/housedev/nestorage/<dir>
make hooks   # once per clone: installs the Lefthook Git hooks
# ... edit ...
make build   # type-check the module (no binary artifact exists yet)
make test    # run tests with the race detector; writes coverage.out
make lint    # static analysis (golangci-lint)
make fmt     # apply the configured formatters in place
```

Every target that exists on `main` today, as printed by `make help`:

```sh
make build      # type-check the module (a library emits no binary artifact yet)
make test       # run the test suite with the race detector and write a coverage profile
make cover      # summarise the coverage profile written by `make test`
make lint       # run golangci-lint using the pinned version
make fmt        # apply the configured formatters in place
make tidy       # prune and refresh go.mod / go.sum
make hooks      # install the Lefthook git hooks
make hooks-uninstall  # remove the Lefthook git hooks
make clean      # remove build artifacts
make help       # list available targets
```

### What the hooks do

- `commit-msg` runs `conform`, validating the message against
  [Conventional Commits](https://www.conventionalcommits.org) before the
  commit is created.
- `pre-commit` runs `fmt`, then `lint`, then a fast `go test ./...`, piped so
  it stops at the first failure.
- `pre-push` runs the full race-enabled test suite and lint over the whole
  module before anything leaves the machine.

### Not yet available

The Postgres compose service, database migrations, runtime configuration,
and the server entrypoint arrive in NSTR-14 and NSTR-15 — there is
intentionally no way to run the app yet.

## License

[AGPL-3.0](LICENSE), matching Nestova and nestcore.

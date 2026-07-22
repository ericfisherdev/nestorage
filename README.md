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

Everything else — lefthook, conform, templ — is pinned in `go.mod` via Go
tool directives, so no global install is needed: invoke it with
`go tool <name>`. The front-end has no Node toolchain: `make assets`
downloads the pinned, checksum-verified
[Tailwind v4 standalone CLI](https://tailwindcss.com/blog/standalone-cli) on
first use.

### The local dev loop

```sh
git -C /home/esfisher/dev/housedev/nestorage worktree add <dir> -b <branch>
cd /home/esfisher/dev/housedev/nestorage/<dir>
make hooks   # once per clone: installs the Lefthook Git hooks
# ... edit ...
make build   # build the CSS bundle, then compile bin/server
make test    # run tests with the race detector; writes coverage.out
make lint    # static analysis (golangci-lint)
make fmt     # apply the configured formatters in place
```

Every target that exists on `main` today, as printed by `make help`:

```sh
make build      # build assets then compile the server binary into ./bin
make assets     # build the Tailwind CSS bundle (downloads the pinned CLI if missing)
make run        # run the server from source
make test       # run the test suite with the race detector and write a coverage profile
make test-gated # run the database-gated suites (needs NESTORAGE_TEST_DATABASE_URL)
make cover      # summarise the coverage profile written by `make test`
make lint       # run golangci-lint using the pinned version
make fmt        # apply the configured formatters in place
make generate   # generate Go code from .templ files
make migrate-up      # apply all pending database migrations
make migrate-down    # roll back the most recent migration
make migrate-status  # show the migration status
make migrate-reset   # roll back all migrations
make migrate-create  # scaffold a new migration (usage: make migrate-create name=add_widgets)
make tidy       # prune and refresh go.mod / go.sum
make hooks      # install the Lefthook git hooks
make hooks-uninstall  # remove the Lefthook git hooks
make clean      # remove build artifacts
make help       # list available targets
```

The `migrate-*` targets invoke `./cmd/migrate`, which does not exist yet
(NSTR-14). They are harmless until then — nothing in CI or the hooks calls
them.

### Gated tests

`make test` is hermetic — no database, no network. Suites that need a real
Postgres are gated behind `NESTORAGE_TEST_DATABASE_URL` and run separately
with `make test-gated`, which fails fast with a clear message if the
variable is unset. `GATED_TEST_PACKAGES` in the `Makefile` is empty until
Sprint 4 (Bins & Items) adds the first adapter suite.

### Git hooks (Lefthook)

[Lefthook](https://lefthook.dev) is pinned as a Go tool directive in
`go.mod`. Enable the hooks once per clone:

```sh
make hooks            # go tool lefthook install
make hooks-uninstall  # remove them (lefthook.yml itself is left in place)
```

The hooks delegate to the same `make` targets as CI, so local and CI checks
never diverge ([`lefthook.yml`](lefthook.yml)):

- **commit-msg**: enforce [Conventional Commits](https://www.conventionalcommits.org)
  ([`.conform.yaml`](.conform.yaml)) before the commit is created.
- **pre-commit** (piped, fails fast): format staged `.go`/`.templ` sources,
  verify generated `*_templ.go` is in sync, `make lint`, then a fast
  `go test ./...`.
- **pre-push**: the full race-enabled `make test` plus `make lint` over the
  whole module before anything leaves the machine.

This repo uses the [bare-clone-plus-worktrees layout](#repository-layout)
above, and hooks install into the repository-wide `.bare/hooks` directory —
`git rev-parse --git-path hooks` resolves through `--git-common-dir`, which
every worktree shares. So `make hooks` is run once per clone, not once per
worktree, and it covers every existing and future per-ticket worktree.

### Not yet available

`cmd/server` is a placeholder that does nothing but exist, so `make build`
has something to compile — NSTR-15 fills in config loading and the HTTP
server bootstrap. The Postgres compose service and database migrations
arrive in NSTR-14. There is intentionally no way to run the app yet.

## License

[AGPL-3.0](LICENSE), matching Nestova and nestcore.

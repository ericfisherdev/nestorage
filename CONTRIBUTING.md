# Contributing

## Development setup

See the [README](README.md) for the toolchain and `make` targets. Enable the
Git hooks once per clone with `make hooks`.

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org):

```text
<type>(<optional scope>): <description>
```

- **Allowed types:** `feat`, `fix`, `docs`, `chore`, `refactor`, `test`,
  `build`, `ci`, `perf`, `style`, `revert`.
- **Scope** is optional; the project uses the Jira issue key, e.g.
  `feat(NSTR-10): ...`.
- The description is lower-case and has no trailing period.

Enforcement is automated (policy in [`.conform.yaml`](.conform.yaml), via
[conform](https://github.com/siderolabs/conform)):

- Locally, the Lefthook `commit-msg` hook rejects a non-conforming message
  before the commit is created (`make hooks` to enable).
- In CI, the `commit-lint` job validates every commit a PR adds, and the
  `pr-title` check validates the PR title (used as the subject on a squash
  merge) — this is the most common CI failure, so double-check the title
  before pushing.

Examples — good: `fix(NSTR-2): correct the badger-isolation depguard glob`;
bad: `Added stuff.` (no type, capitalised, trailing period).

## Branch protection & merging

`main` is protected. Pull requests cannot be merged until:

- The **`build`** and **`commit-lint`** status checks pass. These are jobs in
  [`.github/workflows/ci.yml`](.github/workflows/ci.yml): `build` runs the
  same gates as the local hooks (lint, formatting, tests), and `commit-lint`
  enforces Conventional Commits.
- The branch is **up to date with `main`** (strict mode) — rebase onto the
  latest `main` if it has moved on.
- **All review conversations are resolved.**

**Required approving reviews: 0.** Review is still expected in practice; it
is simply not mechanically enforced, and there is no CodeRabbit approval
gate. Stale reviews are dismissed on new commits regardless.

The `lint` and `SonarQube` CI jobs run on every PR but are **not** required
contexts — they report status without blocking a merge.

Admin enforcement is intentionally **off** (`enforce_admins=false`) so the
solo maintainer can merge their own PRs. This is not a license to bypass the
checks — the required checks and conversation resolution still apply to the
normal flow.

PRs open as **drafts** and target `ericfisherdev/nestorage`. Merge with
**rebase and merge** to keep a linear history, then delete the branch.

# Shared Taskfiles

`go.yml` holds the tasks any High Five Labs Go repository can use. It names
nothing of this repository: the repository's own `Taskfile.yml` includes it
and passes the variables it needs.

| Task | What it does |
| --- | --- |
| `go:install-tool` | `go install` a pinned tool into `bin/`, once (`NAME`, `PKG`) |
| `go:fmt-check` | fail if a Go file under `DIRS` needs gofmt |
| `go:committed` | fail if generated output under `PATHS` differs from the index or is untracked |
| `go:test-envtest` | `go test` with envtest's assets found (download retried), or fail rather than skip |

## How other repositories could share it

Not decided; this is the recommendation. No repository has been created.
The file would live in one repository (call it `taskfiles`) at a tag, and
config-connector-extensions, aso-extensions and the rest would take it
from there.

**Vendored copy with a sync script (recommended for now).** Each repository
commits `taskfiles/` and a `hack/sync-taskfiles.sh` that copies it from the
shared repository at a tag it names, with a `--check` mode `task check`
runs, as `UPSTREAM.md` pins ACK's code generator and the method is copied
into Bundt.

- For: works offline and on a Spot VM with no credentials; a change to the
  shared file shows up as a diff in each repository's pull request; no
  reliance on how Task fetches.
- Against: copies drift until someone syncs (the `--check` mode and a
  tag bump make it visible, not automatic); a fix is made upstream and then
  copied N times.

**Task's remote includes from a private repository.** `includes:
go: {taskfile: <git or https URL>, checksum: ...}` (Task 3.54 has remote
Taskfiles outside the experiments; `task --download` and `--offline`
control the cache).

- For: one copy, nothing to sync; a checksum pins what was fetched.
- Against: every run, VM and CI job needs network and credentials to the
  private repository (offload's VMs have none by default; a git-over-SSH
  URL needs a key, an HTTPS raw URL a token Task cannot send as a
  header, as far as I could tell); an offline laptop works only from the
  cache; a bad tag or a revoked token breaks every repository at once; it
  is untested here, since no such repository exists.

Move to remote includes if the Spot VMs get read access to the shared
repository and the cache and `--offline` behave in practice; the include
line is the only thing that changes.

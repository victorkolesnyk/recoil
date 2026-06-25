# Contributing

recoil is small on purpose. For anything bigger than a fix, open an issue first
so we can agree it fits before you write it.

## Keep these constraints

- **Stdlib only.** No third-party dependencies — `go.mod` has no `require` block
  and should stay that way.
- **No embeddings, no network, no model calls.** Recall is deterministic keyword
  overlap. That's the whole point of the tool.
- **One binary, one plain-text store.** A user should be able to read and edit
  the store by hand.

## Before a PR

- `go build ./...` and `go test ./...` pass.
- `go vet ./...` is clean.
- New behavior has a test. The scoring and parsing logic is pure and easy to
  test — see `main_test.go`.

## Style

Plain, idiomatic Go. Comments explain *why*, not *what*.

# Agent instructions

A fleeting plugin for OpenStack, published as open source. Read `README.md`
for what it does. The code is held to the standard of GitLab's own fleeting
plugins: small, explicit, tested. A change that adds lines must earn them.

## Layout

- `provider.go`: `InstanceGroup`, the `provider.InstanceGroup` implementation.
  It embeds `Config` and holds the runtime state.
- `config.go`: `Config`, the `plugin_config`, with its validation.
- `state.go`: Nova status to fleeting state, and health.
- `backoff.go`: the wait before creating servers again after new ones failed.
- `address.go`: which address the runner connects to.
- `internal/cloud`: the only package that imports gophercloud, with the retry
  policy for API calls (`retry.go`). The provider sees the narrow `Client`
  interface; `internal/cloud/fake` implements it in memory for tests.
- `cmd/fleeting-plugin-openstack`: `plugin.Main`, nothing else.

## Scope

The plugin speaks the OpenStack APIs and nothing else. No behavior specific to
a cloud provider: what differs between clouds is configuration, and examples
and fixtures use generic names (`RegionOne`, `m1.large`, `public`). Notes for
one cloud go in its guide, under `docs/clouds/`.

## Fleeting semantics to keep in mind

- Fleeting prunes an instance reported as `deleted` or `timeout` without calling
  `Decrease`. A server the plugin still pays for is never reported that way:
  report it as `deleting`, and delete it.
- `Update` is called often (every 2 seconds while an instance is expected).
  Keep it to one list call, plus deletions of broken servers.
- The taskscaler only backs off when a running instance fails its readiness
  check. Servers that fail before running are the plugin's to space out
  (`backoff.go`).
- Fleeting has no timeout for instances stuck creating: `build_timeout` is it.
- Over gRPC, fleeting drops the result of a call that returns an error. An
  `Increase` or `Decrease` that partly succeeded returns its result without an
  error, and logs the failures. Unit tests call the methods directly, so they
  cannot catch this: keep the rule in mind.
- `Heartbeat` errors mark an instance unhealthy and lead to its replacement.
  Return one only for a fact about the server, never for a failed API call.

## Code

- Wrap every error with the object and the action, in lowercase:
  `fmt.Errorf("delete server %s: %w", id, err)`. Wrap `cloud.ErrNotFound`
  rather than comparing strings.
- Thread `context.Context` through every call. No panics, no goroutines
  without a reason written next to them.
- Doc comments on every exported identifier, saying what it is for. Comments
  elsewhere explain a why the code cannot show.
- No commented-out code, no TODO without an issue link, no `nolint` without
  an explanation.
- Dependencies stay at `fleeting`, `gophercloud`, `hclog` and `testify`. Ask before
  adding one.

## Tests

- State shared between plugin calls sits behind a mutex, even where fleeting
  calls one at a time: it does not at shutdown.
- No package-level state to swap in tests: a dependency the tests replace is a
  field (`InstanceGroup.connect`, `retrier.delay`) with a production default.
- Tests that depend on time run in `testing/synctest`, and move the clock with
  `time.Sleep`.
- Table-driven, with testify (`require` for preconditions, `assert` for
  checks). A behavior in `provider.go` is tested against the fake; a request or
  response shape in `internal/cloud` against an HTTP fixture.
- Never call a real cloud from a unit test. The integration test does that,
  only when `FLEETING_OPENSTACK_CLOUD` is set.

## Commits

Conventional Commits (`feat:`, `fix:`, `docs:`, `chore(deps):`...): the
release notes are generated from them by git-cliff (`cliff.toml`). A user-facing
change is a `feat` or a `fix`, with a message a user understands.

## Tools

Dependabot does not update `mise.toml`. When touching the repository, run
`mise outdated`, and bump the tools with `mise upgrade --bump` then `mise lock`
in a separate `chore(deps):` commit, with `mise.lock`.

## Checks

Run `mise run lint`, `mise run test` and `mise run vuln` before declaring work
done; CI runs the same tasks. `mise run fix` formats and applies safe fixes.

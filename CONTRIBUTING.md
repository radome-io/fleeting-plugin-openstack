# Contributing

Issues and pull requests are welcome. For a bug, include the plugin and GitLab
Runner versions, the OpenStack release or cloud, your `plugin_config` without
credentials, and the runner's logs.

## Development

The tools are pinned in `mise.toml`:

```shell
mise install
mise run lint       # golangci-lint, formatting included
mise run test       # unit tests, with the race detector
mise run vuln       # govulncheck
mise run dist       # release binaries in dist/
mise run package    # the GitHub release files in release/
mise run changelog  # release notes of the unreleased commits
```

`AGENTS.md` holds the code conventions, for people and coding agents alike.

Dependabot updates the Go modules and the GitHub Actions, not the tools of
`mise.toml`: check them with `mise outdated`, bump them with
`mise upgrade --bump`, then run `mise lock` to update `mise.lock` for every
platform it lists. CI installs exactly the versions and checksums of
`mise.lock`. A new Go release often fixes standard-library vulnerabilities that
`mise run vuln` would otherwise report.

## Integration test

`mise run integration` runs fleeting's provisioning test against a real cloud.
It creates up to three billed servers, scales them down to zero and runs a
command over SSH. It is not required for a pull request. It reads:

| Variable | Description |
| --- | --- |
| `FLEETING_OPENSTACK_CLOUD` | The entry of `clouds.yaml`. The test is skipped without it. |
| `FLEETING_OPENSTACK_CLOUDS_CONFIG` | The path to `clouds.yaml`, if not in a standard location. |
| `FLEETING_OPENSTACK_FLAVOR` | A flavor. |
| `FLEETING_OPENSTACK_IMAGE` | An image with SSH. |
| `FLEETING_OPENSTACK_NETWORK` | A network the test can reach the servers on. |
| `FLEETING_OPENSTACK_KEY_NAME` | A Nova key pair. |
| `FLEETING_OPENSTACK_SSH_USERNAME` | The image's SSH user. |
| `FLEETING_OPENSTACK_SSH_KEY_FILE` | The private key of the key pair. |

When it ends, even on a failure, the test deletes every server left with its
tag. If that fails too, it names the tag to clean up by hand.

### With fnox

[fnox](https://fnox.jdx.dev) can fetch these from a secret manager, writing
`clouds.yaml` and the SSH key to temporary files deleted after the run. For
example with 1Password, in a `fnox.local.toml`, which Git ignores:

```toml
[providers.op]
type = "1password"
vault = "Private"

[secrets]
FLEETING_OPENSTACK_CLOUD = { default = "mycloud" }
FLEETING_OPENSTACK_CLOUDS_CONFIG = { provider = "op", value = "OpenStack CI/clouds.yaml", as_file = true }
FLEETING_OPENSTACK_SSH_KEY_FILE = { provider = "op", value = "OpenStack CI/private key", as_file = true }
FLEETING_OPENSTACK_SSH_USERNAME = { default = "ubuntu" }
FLEETING_OPENSTACK_KEY_NAME = { default = "fleeting-test" }
FLEETING_OPENSTACK_FLAVOR = { default = "m1.small" }
FLEETING_OPENSTACK_IMAGE = { default = "Ubuntu 24.04" }
FLEETING_OPENSTACK_NETWORK = { default = "public" }
```

```shell
fnox exec -- mise run integration
```

## Pull requests

- One change per pull request, with tests for the behavior it adds or fixes.
- Pull requests are squashed, and the title becomes the commit message: write
  it as a [Conventional Commit](https://www.conventionalcommits.org/), since the
  release notes are generated from it. A change users see is a `feat` or a
  `fix`; a breaking one carries a `!`, as in `feat!: drop the max_size key`.
- The plugin uses the OpenStack APIs only: no behavior specific to one cloud.

## Releasing

Pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`. The build and
tests run in a job with a read-only token; a separate job, which runs no
project code, publishes and signs.

GitHub creates a package as private. After the first release, make
`fleeting-plugin-openstack` public in the package settings, or no one else can
install it.

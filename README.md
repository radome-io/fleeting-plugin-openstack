# fleeting-plugin-openstack

A [fleeting](https://gitlab.com/gitlab-org/fleeting/fleeting) plugin for
OpenStack. It lets GitLab Runner's
[Docker Autoscaler](https://docs.gitlab.com/runner/executors/docker_autoscaler/)
and [Instance](https://docs.gitlab.com/runner/executors/instance/) executors
run jobs on servers they create on demand and delete when idle.

OpenStack has no autoscaling group: the plugin creates and deletes servers
itself, and finds the servers of a group by a Nova tag. It only uses the
standard OpenStack APIs, through
[gophercloud](https://github.com/gophercloud/gophercloud).

The plugin is in early development. Until 1.0, a release may change the
configuration, and its release notes say so.

## Features

| Feature | Support |
| --- | --- |
| Scaling down to zero | ✅ |
| SSH | ✅ |
| Windows and WinRM | ⚠️ Untested |
| Password and application credentials | ✅ |
| Flavors, images, networks, security groups and server groups by name or ID | ✅ |
| New images without a restart | ✅ |
| User data, config drive, tags and metadata | ✅ |
| IPv4 and IPv6 | ✅ |
| Floating IPs | ✅ When the cloud attaches them |
| Multi-arch servers (amd64, arm64) | ✅ |
| Broken server cleanup, backoff and retries | ✅ |
| Boot from volume | ✅ |
| Credentials provisioned per server | ❌ |
| Suspend and resume | ❌ |

## Requirements

- Nova API microversion 2.52 or later, 2.67 with `server.volume_type`.
- An OpenStack user that can create, list and delete servers, list flavors,
  networks, security groups and server groups, and read images and key pairs.
  With `server.volume_size`, it must also be able to create volumes, within
  the volume quota.
- An image that can run your jobs, or a stock image with user data that
  installs what they need, such as Docker.
- An SSH key or password the image accepts. The plugin does not provision
  credentials: set `use_static_credentials = true` in `connector_config`.

## Installation

Reference the plugin in the runner's `config.toml`, then run
`gitlab-runner fleeting install`:

```toml
[runners.autoscaler]
  plugin = "ghcr.io/radome-io/fleeting-plugin-openstack:0.1"
```

## Configuration

Guides for specific clouds: [OVHcloud Public Cloud](docs/clouds/ovh.md).

The plugin authenticates with a
[`clouds.yaml`](https://docs.openstack.org/python-openstackclient/latest/configuration/index.html)
file, by password or application credential:

```yaml
clouds:
  mycloud:
    auth_type: v3applicationcredential
    auth:
      auth_url: https://identity.example.com/v3
      application_credential_id: "..."
      application_credential_secret: "..."
    region_name: RegionOne
```

A runner using it, with one job per server at a time:

```toml
[[runners]]
  executor = "docker-autoscaler"

  [runners.docker]
    image = "alpine:latest"

  [runners.autoscaler]
    plugin = "ghcr.io/radome-io/fleeting-plugin-openstack:0.1"
    capacity_per_instance = 1
    max_use_count = 10
    max_instances = 4
    instance_ready_command = "cloud-init status --wait"

    [runners.autoscaler.plugin_config]
      cloud = "mycloud"
      clouds_config = "/etc/gitlab-runner/clouds.yaml"
      name = "runner"

      [runners.autoscaler.plugin_config.server]
        flavor = "m1.large"
        image = "Ubuntu 24.04"
        networks = ["public"]
        user_data_file = "/etc/gitlab-runner/cloud-init.yaml"

    [runners.autoscaler.connector_config]
      username = "runner"
      key_path = "/etc/gitlab-runner/id_ed25519"
      use_static_credentials = true
      use_external_addr = true

    [[runners.autoscaler.policy]]
      idle_count = 0
      idle_time = "30m"
```

And `/etc/gitlab-runner/cloud-init.yaml`, which `instance_ready_command` waits
for:

```yaml
#cloud-config
users:
  - name: runner
    groups: [docker]
    ssh_authorized_keys: ["ssh-ed25519 AAAA..."]
runcmd:
  - curl -fsSL https://get.docker.com | sh
```

### `plugin_config`

| Setting | Default | Description |
| --- | --- | --- |
| `cloud` | Required | The `clouds.yaml` entry. |
| `clouds_config` | Standard locations | The path to `clouds.yaml`. `OS_REGION_NAME` and `OS_INTERFACE` in the runner's environment override its region and interface. |
| `name` | Required | The group name. Servers are named `<name>-<random>`. 1 to 54 lowercase letters, digits or dashes; 51 at most with the default `tag`. |
| `tag` | `fleeting:<name>` | The Nova tag marking the group's servers. The plugin may delete any server carrying it, so give each runner manager in a project its own. |
| `server.flavor` | Required | Name or ID. |
| `server.image` | Required | Name or ID. A name resolves at each scale-up to the newest active image with that name, public images included: publishing a new image rolls it out. Use an ID to pin one. |
| `server.volume_size` | | Boot from a new volume of this size, in GiB, made from the image and deleted with the server: for flavors without a root disk, or for a larger disk. |
| `server.volume_type` | | The volume type, with `server.volume_size`. Needs Nova API microversion 2.67. |
| `server.networks` | Required | Names or IDs. |
| `server.security_groups` | | Names or IDs. |
| `server.key_name` | | A Nova key pair. |
| `server.user_data` | | User data, passed as is. An inline cloud-config must start at the first column: TOML keeps the indentation of multi-line strings. |
| `server.user_data_file` | | A file to read the user data from, at startup. Excludes `server.user_data`. |
| `server.config_drive` | `false` | Attach a config drive with the user data and metadata, for clouds without a metadata service. |
| `server.tags` | | Extra Nova tags. |
| `server.metadata` | | Server metadata. Keys of 1 to 255 characters, values of up to 255. |
| `server.availability_zone` | | Where servers are created. |
| `server.server_group` | | Name or ID of a Nova server group the servers join. Its `server_group_members` quota, often 10, then limits `max_instances`, and a hard `anti-affinity` policy needs a host per server: prefer `soft-anti-affinity`. |
| `address_network` | | The network to connect on, by name or ID, one of `server.networks`. Required when servers have addresses on several networks. |
| `address_family` | `ipv4` | `ipv4` or `ipv6`. |
| `build_timeout` | `15m` | How long a server may stay in `BUILD` before it is deleted. |

The plugin refuses to start on an unknown key, when the flavor, a network,
a security group, the server group, the key pair or the image does not exist,
or when `address_network` is not one of `server.networks`.

The runner's `max_instances` limits the group, up to 1000 servers.

### Connections

The internal address is a server's first fixed address of `address_family`,
and the external address its first floating one, or the fixed one without a
floating IP. `use_external_addr` makes the runner use the external one.

When `connector_config` sets no `os` or `arch`, the plugin reads them from the
image's `os_type` and `architecture` properties, and defaults to linux and
amd64. A server booted from a volume lists no image: the plugin reads the
properties of `server.image` instead. The protocol defaults to SSH, or WinRM
for Windows.

## How it works

Servers in `BUILD` are creating. Servers in `ACTIVE`, `MIGRATING`, `REBOOT` or
`HARD_REBOOT` are running. The plugin deletes any other server, such as one in
`ERROR` or `SHUTOFF`, or still in `BUILD` after `build_timeout`, and retries
failed deletions every minute.

Servers are created and deleted one at a time, up to the first failure, such
as an exceeded quota. The runner asks again for the rest.

When new servers cannot be created or fail to come up, the plugin pauses
scale-ups for 30 seconds, then tries a single server. Each failure in a row
doubles the pause, up to 10 minutes, and a server that comes up ends it. The
plugin logs why it pauses.

A server fails its heartbeat when it is gone or no longer running, not when
the API call fails. Fleeting drops a server missing from the listings: at once
if it was being deleted, and after six listings in a row otherwise, up to six
minutes when the group is idle. Until then, the runner may give a job to a
server deleted outside the plugin, unless
`FF_USE_FLEETING_ACQUIRE_HEARTBEATS` makes it check the server first. A server
Nova never finishes deleting stays in the group until you delete it.

API calls are retried up to three times, 1, 2 then 4 seconds apart, when the
cloud answers 429 (after its `Retry-After`, up to 30 seconds). Calls that are
safe to repeat are also retried on 502, 503, 504 and network errors; server
creations are not. Each attempt times out after a minute.

## Releases

Releases are published to `ghcr.io/radome-io/fleeting-plugin-openstack`,
signed with cosign, and to GitHub Releases with binaries, SBOMs and build
provenance. To verify them:

```shell
cosign verify ghcr.io/radome-io/fleeting-plugin-openstack:X.Y.Z \
  --certificate-identity-regexp '^https://github.com/radome-io/fleeting-plugin-openstack/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify fleeting-plugin-openstack-linux-amd64 --repo radome-io/fleeting-plugin-openstack
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), and [SECURITY.md](SECURITY.md) to
report a vulnerability.

## License

[MIT](LICENSE)

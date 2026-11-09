# OVHcloud Public Cloud

The plugin runs on [OVHcloud Public Cloud](https://www.ovhcloud.com/en/public-cloud/)
without anything specific to it. This guide gives the values that work there.

## Project and user

Give CI a Public Cloud project of its own. The plugin may delete any server
carrying its tag, and the quotas and bill stay separate from your other
workloads.

In the project, under **Project Management → Users**, create a user with the
**Compute Operator** role only. The password is shown once.

OVHcloud's IAM service accounts sign in with OpenID Connect, which the plugin
does not support: use this OpenStack user.

## `clouds.yaml`

In Horizon (on the user's row, **...** → **Launch OpenStack Horizon**), **API
Access** → **Download OpenStack clouds.yaml File** gives the user's entry,
without its password. Add the password to its `auth`, then create an
application credential for the plugin, limited to Compute Operator:

```shell
openstack --os-cloud openstack application credential create fleeting \
  --role compute_operator -f yaml -c id -c secret
```

Replace the entry's `auth` with the credential, and set `auth_type`. The
password is no longer needed. This guide names the entry `ovh`:

```yaml
clouds:
  ovh:
    auth_type: v3applicationcredential
    auth:
      auth_url: https://auth.cloud.ovh.net/
      application_credential_id: ...
      application_credential_secret: "..."
    region_name: "GRA11"
    interface: "public"
    identity_api_version: 3
```

Make the file readable by the runner's user only.

## Network and security group

Servers attached to `Ext-Net`, the public network, get a public IPv4 and IPv6
address on it, and no floating IP is needed. The images only configure IPv4,
so keep the default `address_family = "ipv4"`.

These servers are reachable from the Internet, and the project's `default`
security group lets all traffic in. Create a group that allows SSH from the
runner manager only, and set it in `security_groups`: Nova then applies it
instead of `default`. The plugin's credential cannot create it: use an account
with the **Network Security Operator** role.

```shell
openstack security group create fleeting-ssh
openstack security group rule create fleeting-ssh \
  --protocol tcp --dst-port 22 --remote-ip 203.0.113.10/32
```

## Flavors, images and billing

`openstack flavor list` lists the flavors of the region: the `b3-*` general
purpose and `c3-*` compute flavors suit CI. `Ubuntu 24.04` is a public image
that OVHcloud updates; by name, the plugin uses the newest one at each
scale-up.

Outside the US, servers are billed by the second from `ACTIVE`, with a
one-minute minimum. An idle server costs as much as a busy one: keep
`idle_time` short. Reusing servers with `max_use_count` saves boot time and
keeps their Docker cache warm.

New projects have low quotas. Check them under **Project Management → Quota
and Regions** against `max_instances`.

## Runner configuration

```toml
[runners.autoscaler]
  plugin = "ghcr.io/radome-io/fleeting-plugin-openstack:0.1"
  capacity_per_instance = 1
  max_use_count = 20
  max_instances = 4
  instance_ready_command = "cloud-init status --wait"

  [runners.autoscaler.plugin_config]
    cloud = "ovh"
    clouds_config = "/etc/gitlab-runner/clouds.yaml"
    name = "runner"

    [runners.autoscaler.plugin_config.server]
      flavor = "b3-8"
      image = "Ubuntu 24.04"
      networks = ["Ext-Net"]
      security_groups = ["fleeting-ssh"]
      user_data_file = "/etc/gitlab-runner/cloud-init.yaml"

  [runners.autoscaler.connector_config]
    username = "runner"
    key_path = "/etc/gitlab-runner/id_ed25519"
    use_static_credentials = true

  [[runners.autoscaler.policy]]
    idle_count = 0
    idle_time = "10m"
```

The cloud-init file is the one in the [README](../../README.md#configuration).

## Checking a project

From a checkout of this repository, the integration test creates up to three
`b3-8` servers in the project, connects to them over SSH and deletes them.
Nova key pairs belong to a user: create the test's with the plugin's
credentials.

```shell
ssh-keygen -t ed25519 -f ~/.ssh/fleeting-test -N ''
openstack --os-cloud ovh keypair create --public-key ~/.ssh/fleeting-test.pub fleeting-test

export FLEETING_OPENSTACK_CLOUD=ovh
export FLEETING_OPENSTACK_FLAVOR=b3-8
export FLEETING_OPENSTACK_IMAGE="Ubuntu 24.04"
export FLEETING_OPENSTACK_NETWORK=Ext-Net
export FLEETING_OPENSTACK_KEY_NAME=fleeting-test
export FLEETING_OPENSTACK_SSH_USERNAME=ubuntu
export FLEETING_OPENSTACK_SSH_KEY_FILE=~/.ssh/fleeting-test
mise run integration
```

Set `FLEETING_OPENSTACK_CLOUDS_CONFIG` when `clouds.yaml` is not in a standard
location.

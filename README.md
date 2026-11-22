<p align="center">
  <img src=".github/assets/banner.png" alt="ProxCenter Terraform Provider - Infrastructure as Code" width="100%">
</p>

<p align="center">
  <strong>Manage ProxCenter virtual machines as code, from a provider administrator down to a vDC tenant</strong>
</p>

<p align="center">
  <a href="https://registry.terraform.io/providers/adminsyspro/proxcenter/latest"><img src="https://img.shields.io/badge/Terraform%20Registry-adminsyspro%2Fproxcenter-E57000?style=flat-square&logo=terraform&logoColor=white&labelColor=1C1C24" alt="Terraform Registry"></a>
  <a href="https://github.com/adminsyspro/terraform-provider-proxcenter/releases"><img src="https://img.shields.io/github/v/release/adminsyspro/terraform-provider-proxcenter?style=flat-square&label=Release&color=E57000&labelColor=1C1C24" alt="Latest release"></a>
  <a href="https://developer.hashicorp.com/terraform/install"><img src="https://img.shields.io/badge/Terraform-1.11%2B-F28C28?style=flat-square&logo=terraform&logoColor=white&labelColor=1C1C24" alt="Terraform 1.11 or later"></a>
  <a href="https://www.proxcenter.io/"><img src="https://img.shields.io/badge/ProxCenter-1.5%2B-F28C28?style=flat-square&labelColor=1C1C24" alt="ProxCenter 1.5 or later"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MPL--2.0-FFC48A?style=flat-square&labelColor=1C1C24" alt="License MPL-2.0"></a>
</p>

<p align="center">
  <a href="https://github.com/adminsyspro/terraform-provider-proxcenter/actions/workflows/test.yml"><img src="https://img.shields.io/github/actions/workflow/status/adminsyspro/terraform-provider-proxcenter/test.yml?branch=main&style=flat-square&logo=github&label=Tests&labelColor=1C1C24" alt="Tests"></a>
  <a href="https://github.com/adminsyspro/terraform-provider-proxcenter/actions/workflows/security-scan.yml"><img src="https://img.shields.io/github/actions/workflow/status/adminsyspro/terraform-provider-proxcenter/security-scan.yml?branch=main&style=flat-square&logo=github&label=Security%20Scan&labelColor=1C1C24" alt="Security Scan"></a>
</p>

<p align="center">
  <a href="https://www.proxcenter.io/"><b>🌐 Website</b></a> · <a href="https://registry.terraform.io/providers/adminsyspro/proxcenter/latest/docs"><b>📖 Provider documentation</b></a> · <a href="https://docs.proxcenter.io/"><b>📚 ProxCenter documentation</b></a>
</p>

---

## Overview

<p align="center">
  The <strong>ProxCenter provider</strong> drives <a href="https://www.proxcenter.io/">ProxCenter</a>, the Proxmox VE datacenter management platform, through its public API v1. Describe your virtual machines once, then let Terraform create them from the catalogue or by cloning, change them in place and follow what changed outside it.
</p>

It talks to ProxCenter only, with a `pxc_` API token: the same configuration works for a provider administrator and for the tenant of a Virtual Data Center. Quotas, allowed networks and the vDC boundaries stay enforced by ProxCenter, and its refusals reach you as it wrote them.

---

## Requirements

- **Terraform** 1.11 or later (the cloud-init password is a write-only attribute)
- **ProxCenter** 1.5.0 or later, Enterprise with the REST API add-on
- **An API token** created in *Settings > API tokens*, with the scopes below

| Scope | Lets the provider |
|---|---|
| `vms:read` | read VMs, the vDC, the catalogue and the blueprints |
| `vms:write` | create, clone and change VMs |
| `vms:power` | start, stop and reboot them |
| `vms:delete` | destroy them |
| `vms:nic` | set a VLAN tag or a MAC address as a vDC tenant |
| `storage:read`, `vdcs:read` | only for the `proxcenter_storages` and `proxcenter_vnets` data sources |

---

## Quick Start

```terraform
terraform {
  required_providers {
    proxcenter = {
      source  = "adminsyspro/proxcenter"
      version = "~> 0.1"
    }
  }
}

provider "proxcenter" {
  endpoint = "https://proxcenter.example.com" # or PROXCENTER_ENDPOINT
  token    = var.proxcenter_token             # or PROXCENTER_TOKEN
}

data "proxcenter_vdc" "prod" {
  name = "prod"
}

resource "proxcenter_vm" "web" {
  connection_id = data.proxcenter_vdc.prod.connection_id
  target_node   = data.proxcenter_vdc.prod.nodes[0]
  name          = "web-01"

  source {
    image = "debian-12" # or blueprint = "<id>", or clone_of = <vmid>
  }

  cores     = 2
  memory_mb = 2048

  disk {
    slot    = "scsi0"
    storage = data.proxcenter_vdc.prod.primary_storage
    size_gb = 20
  }

  network {
    bridge = "vnet-prod"
  }

  cloud_init {
    user     = "ops"
    ssh_keys = [file("~/.ssh/id_ed25519.pub")]
  }

  power_state = "running"
}

output "web_address" {
  value = proxcenter_vm.web.cloud_init.ip_address
}
```

```bash
export PROXCENTER_TOKEN=pxc_...
terraform init
terraform apply
```

---

## What it manages

| | Name | Purpose |
|---|---|---|
| Resource | `proxcenter_vm` | QEMU VM from a catalogue image, a blueprint or a clone; CPU, memory, disks, network cards, cloud-init and power changed in place; importable |
| Data source | `proxcenter_vdc`, `proxcenter_vdcs` | vDCs with their nodes, storages, quota and usage |
| Data source | `proxcenter_images`, `proxcenter_blueprints` | what the token may deploy |
| Data source | `proxcenter_storages` | storages of a connection |
| Data source | `proxcenter_vnets` | networks of a vDC, with their subnet and IPAM state |
| Data source | `proxcenter_vms` | VMs of a connection, to find a template to clone |

Every attribute is described in the [provider documentation](https://registry.terraform.io/providers/adminsyspro/proxcenter/latest/docs), generated from this repository's [docs/](docs/).

---

## How changes apply

Production VMs are not cattle: nothing in a configuration change destroys a VM by accident.

| You change | The provider |
|---|---|
| CPU, memory, name, cloud-init | updates the VM in place; a change Proxmox applies at the next reboot warns you, or reboots the VM with `reboot_on_change = true` |
| A disk size | grows the disk; a smaller size is refused at plan, since Proxmox cannot shrink a disk |
| A disk storage | moves the disk to the new storage |
| A network card | rewrites that card only, keeping its MAC address and settings you did not describe |
| `power_state` | starts the VM, or shuts it down by ACPI (hard stop only with `force_stop = true`) |
| `source` | replaces the VM: the only change that does |
| A disk or a card removed from the configuration | refuses at plan: remove it in ProxCenter |

What happens outside Terraform:

- **Changes in the ProxCenter UI** to an attribute you describe show up at the next plan; attributes you do not describe, tags and notes are left alone.
- **A DRS migration** to another node plans nothing: the node is read back on every refresh.
- **A VM deleted outside Terraform** is planned for creation again.
- **The cloud-init password** is write-only: it is never stored in the state. Bump `password_wo_version` to send a new one.

---

## Import

An existing VM is imported by `<connection_id>/<vmid>`:

```bash
terraform import proxcenter_vm.legacy cm4k2abc0000xyz/105
```

Describe every disk and network card it has, with their slots, then `terraform plan` shows no change. A `source` block is not needed for an imported VM.

---

## Development

<details>
<summary><b>Build, test and document</b></summary>

You need Go (the version in `go.mod`) and Terraform 1.11 or later.

```bash
make build     # go build ./...
make test      # unit tests against a simulated ProxCenter
make lint      # golangci-lint
make docs      # regenerate docs/ from the schemas, examples/ and templates/
```

The acceptance tests run against a real ProxCenter and create real VMs. They read their settings from the environment, see [internal/provider/acc_test.go](internal/provider/acc_test.go):

```bash
export PROXCENTER_ENDPOINT=https://proxcenter.example.com PROXCENTER_TOKEN=pxc_...
export PROXCENTER_TEST_CONNECTION=... PROXCENTER_TEST_NODE=pve1 PROXCENTER_TEST_STORAGE=ceph \
       PROXCENTER_TEST_IMAGE=debian-12 PROXCENTER_TEST_BRIDGE=vmbr0
make testacc
```

To try a local build without the registry, point Terraform at it in `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "adminsyspro/proxcenter" = "/path/to/your/go/bin"
  }
  direct {}
}
```

</details>

---

## Security

Every push runs **gitleaks** on the whole history, and the workflows use GitHub-owned actions pinned by commit only, with third-party tools checked against their SHA-256. Releases are signed with GPG. Report vulnerabilities to [security@proxcenter.io](mailto:security@proxcenter.io).

## License

[Mozilla Public License 2.0](LICENSE).

## Support

- Community: [GitHub Issues](https://github.com/adminsyspro/terraform-provider-proxcenter/issues)
- Enterprise: [support@proxcenter.io](mailto:support@proxcenter.io)

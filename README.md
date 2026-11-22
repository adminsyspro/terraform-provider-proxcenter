# Terraform Provider for ProxCenter

Manage [ProxCenter](https://proxcenter.io) virtual machines as code. The provider talks to the ProxCenter public API v1 with a `pxc_` API token, so it works for a provider administrator and for a vDC tenant alike: quotas, allowed networks and the vDC boundaries stay enforced by ProxCenter, and its refusals are shown as they are.

```terraform
provider "proxcenter" {
  endpoint = "https://proxcenter.example.com"
  token    = var.proxcenter_token # or PROXCENTER_ENDPOINT / PROXCENTER_TOKEN
}

data "proxcenter_vdc" "prod" {
  name = "prod"
}

resource "proxcenter_vm" "web" {
  connection_id = data.proxcenter_vdc.prod.connection_id
  target_node   = data.proxcenter_vdc.prod.nodes[0]
  name          = "web-01"

  source {
    image = "debian-12"
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

  power_state = "running"
}
```

## What it manages

- `proxcenter_vm`: a QEMU VM created from the catalogue (an image or a blueprint) or by cloning a VM, then changed in place: CPU, memory, disks (grow and move), network cards, cloud-init, power. It can be imported.
- Data sources: `proxcenter_vdc`, `proxcenter_vdcs`, `proxcenter_images`, `proxcenter_blueprints`, `proxcenter_storages`, `proxcenter_vnets`, `proxcenter_vms`.

The documentation of every resource and data source is in [docs/](docs/).

## Safety

- A disk only grows; shrinking it, or removing a disk or a network card from the configuration, is refused at plan. Only a different `source` replaces a VM.
- A VM moved by DRS to another node plans no change.
- The cloud-init password is write-only: it is never stored in the state.

## Requirements

- ProxCenter 1.5.0 or later.
- Terraform 1.11 or later.
- A token holding `vms:read` and `vms:write`; `vms:power` to run VMs, `vms:delete` to destroy them, `vms:nic` to set a VLAN tag or a MAC address as a vDC tenant.

## Development

```sh
make test      # unit tests against a simulated ProxCenter (downloads nothing but Terraform)
make lint
make docs      # regenerate docs/ from the schemas and examples/
make testacc   # acceptance tests against a real ProxCenter, see internal/provider/acc_test.go
```

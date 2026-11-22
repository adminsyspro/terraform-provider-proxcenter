# A vDC tenant deploys a VM from the catalogue granted to its vDC.
# The token needs vms:read and vms:write; vms:power to run it, vms:delete to
# destroy it, vms:nic to set a VLAN tag or a MAC address.

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
    bridge = "v6bc065f" # pve_name of a VNet of the vDC (data.proxcenter_vnets)
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

# A clone of a template, placed on the template's node.
resource "proxcenter_vm" "app" {
  connection_id = data.proxcenter_vdc.prod.connection_id
  name          = "app-01"

  source {
    clone_of = 9000
  }

  cores     = 2
  memory_mb = 4096

  disk {
    slot    = "scsi0"
    storage = "ceph-gold"
    size_gb = 32
  }

  network {
    bridge = "vnet-prod"
  }

  power_state = "stopped"
}

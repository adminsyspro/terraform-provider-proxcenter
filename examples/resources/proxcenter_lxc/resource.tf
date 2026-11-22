# A vDC tenant creates a container from a template of the cluster.
# The token needs vms:read and vms:write; vms:power to run it, vms:delete to
# destroy it.

data "proxcenter_vdc" "prod" {
  name = "prod"
}

resource "proxcenter_lxc" "web" {
  connection_id = data.proxcenter_vdc.prod.connection_id
  target_node   = data.proxcenter_vdc.prod.nodes[0]
  hostname      = "web-ct-01"

  source {
    ostemplate = "local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst"
  }

  cores     = 2
  memory_mb = 1024

  features = {
    nesting = true
  }

  rootfs {
    storage = data.proxcenter_vdc.prod.primary_storage
    size_gb = 8
  }

  mount_point {
    slot    = "mp0"
    storage = data.proxcenter_vdc.prod.primary_storage
    size_gb = 20
    path    = "/srv"
  }

  network {
    bridge  = "vnet-prod"
    ip      = "10.15.15.40/24" # ProxCenter allocates no IPAM address to a container
    gateway = "10.15.15.1"
  }

  ssh_keys = [file("~/.ssh/id_ed25519.pub")]

  power_state = "running"
}

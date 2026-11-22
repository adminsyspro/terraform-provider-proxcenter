data "proxcenter_vnets" "prod" {
  vdc_id = data.proxcenter_vdc.prod.id
}

output "vnet_bridges" {
  value = { for v in data.proxcenter_vnets.prod.vnets : v.name => v.pve_name }
}

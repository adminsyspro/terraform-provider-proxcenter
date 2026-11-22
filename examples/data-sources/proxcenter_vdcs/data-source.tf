data "proxcenter_vdcs" "all" {}

output "vdc_names" {
  value = data.proxcenter_vdcs.all.vdcs[*].name
}

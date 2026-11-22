data "proxcenter_vdc" "prod" {
  name = "prod"
}

output "nodes" {
  value = data.proxcenter_vdc.prod.nodes
}

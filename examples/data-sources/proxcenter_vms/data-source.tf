data "proxcenter_vms" "templates" {
  connection_id = data.proxcenter_vdc.prod.connection_id
}

output "template_vmids" {
  value = [for vm in data.proxcenter_vms.templates.vms : vm.vmid if vm.template]
}

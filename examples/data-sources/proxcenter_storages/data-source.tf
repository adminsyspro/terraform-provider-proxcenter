data "proxcenter_storages" "prod" {
  connection_id = data.proxcenter_vdc.prod.connection_id
}

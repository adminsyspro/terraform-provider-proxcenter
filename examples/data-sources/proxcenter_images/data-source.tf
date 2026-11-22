data "proxcenter_images" "debian" {
  vendor = "debian"
}

output "debian_images" {
  value = data.proxcenter_images.debian.images[*].slug
}

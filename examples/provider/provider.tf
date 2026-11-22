terraform {
  required_providers {
    proxcenter = {
      source = "registry.proxcenter.io/adminsyspro/proxcenter"
    }
  }
}

# Endpoint and token can also come from PROXCENTER_ENDPOINT and PROXCENTER_TOKEN.
provider "proxcenter" {
  endpoint = "https://proxcenter.example.com"
  token    = var.proxcenter_token
}

variable "proxcenter_token" {
  type      = string
  sensitive = true
}

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestDataVdcByName(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(`data "proxcenter_vdc" "prod" { name = "prod" }`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "id", "vdc1"),
				resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "connection_id", "conn1"),
				resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "pool", "vdc-prod"),
				resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "nodes.#", "2"),
				resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "nodes.1", "pve2"),
				resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "primary_storage", "ceph-gold"),
				resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "quota.max_vms", "7"),
				resource.TestCheckNoResourceAttr("data.proxcenter_vdc.prod", "quota.max_storage_mb"),
				resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "usage.used_vcpus", "4"),
			),
		}},
	})
}

func TestDataVdcByID(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(`data "proxcenter_vdc" "prod" { id = "vdc1" }`),
			Check:  resource.TestCheckResourceAttr("data.proxcenter_vdc.prod", "name", "prod"),
		}},
	})
}

func TestVdcByNameNotFound(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config:      f.config(`data "proxcenter_vdc" "x" { name = "staging" }`),
			ExpectError: regexp.MustCompile(`(?s)No vDC named "staging".*prod`),
		}},
	})
}

func TestVdcNameAndIdConflict(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config:      f.config(`data "proxcenter_vdc" "x" {}`),
				ExpectError: regexp.MustCompile(`Missing Attribute Configuration`),
			},
			{
				Config: f.config(`data "proxcenter_vdc" "x" {
  id   = "vdc1"
  name = "prod"
}`),
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
		},
	})
}

func TestDataVdcs(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(`data "proxcenter_vdcs" "all" {}`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.proxcenter_vdcs.all", "vdcs.#", "1"),
				resource.TestCheckResourceAttr("data.proxcenter_vdcs.all", "vdcs.0.name", "prod"),
				resource.TestCheckResourceAttr("data.proxcenter_vdcs.all", "vdcs.0.quota.max_ram_mb", "65536"),
			),
		}},
	})
}

func TestDataImagesFilteredByVendor(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(`
data "proxcenter_images" "all" {}
data "proxcenter_images" "debian" { vendor = "debian" }`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.proxcenter_images.all", "images.#", "2"),
				resource.TestCheckResourceAttr("data.proxcenter_images.debian", "images.#", "1"),
				resource.TestCheckResourceAttr("data.proxcenter_images.debian", "images.0.slug", "debian-12"),
				resource.TestCheckResourceAttr("data.proxcenter_images.debian", "images.0.is_custom", "false"),
			),
		}},
	})
}

func TestDataBlueprints(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(`data "proxcenter_blueprints" "all" {}`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.proxcenter_blueprints.all", "blueprints.#", "1"),
				resource.TestCheckResourceAttr("data.proxcenter_blueprints.all", "blueprints.0.image_slug", "debian-12"),
				resource.TestCheckResourceAttr("data.proxcenter_blueprints.all", "blueprints.0.description", "Web tier"),
			),
		}},
	})
}

func TestDataStorages(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(`data "proxcenter_storages" "s" { connection_id = "conn1" }`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.proxcenter_storages.s", "storages.#", "2"),
				resource.TestCheckResourceAttr("data.proxcenter_storages.s", "storages.0.storage", "ceph-gold"),
				resource.TestCheckResourceAttr("data.proxcenter_storages.s", "storages.0.shared", "true"),
				resource.TestCheckResourceAttr("data.proxcenter_storages.s", "storages.0.free", "107374182400"),
			),
		}},
	})
}

func TestDataVnets(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(`data "proxcenter_vnets" "n" { vdc_id = "vdc1" }`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.proxcenter_vnets.n", "vnets.#", "1"),
				resource.TestCheckResourceAttr("data.proxcenter_vnets.n", "vnets.0.name", "prod-lan"),
				resource.TestCheckResourceAttr("data.proxcenter_vnets.n", "vnets.0.pve_name", "v6bc065f"),
				resource.TestCheckResourceAttr("data.proxcenter_vnets.n", "vnets.0.cidr", "10.15.15.0/24"),
				resource.TestCheckResourceAttr("data.proxcenter_vnets.n", "vnets.0.ipam_enabled", "true"),
			),
		}},
	})
}

func TestDataVms(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.addVM(9000, &fakeVM{Node: "pve1", Status: "stopped", Config: map[string]any{"name": "tpl-debian", "template": "1"}})
	f.addVM(9101, &fakeVM{Node: "pve2", Status: "running", Config: map[string]any{"name": "web-01"}})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(`
data "proxcenter_vms" "all" { connection_id = "conn1" }
data "proxcenter_vms" "tpl" {
  connection_id = "conn1"
  vmids         = [9000]
}`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.proxcenter_vms.all", "vms.#", "2"),
				resource.TestCheckResourceAttr("data.proxcenter_vms.tpl", "vms.#", "1"),
				resource.TestCheckResourceAttr("data.proxcenter_vms.tpl", "vms.0.template", "true"),
				resource.TestCheckResourceAttr("data.proxcenter_vms.tpl", "vms.0.name", "tpl-debian"),
				resource.TestCheckResourceAttr("data.proxcenter_vms.all", "vms.1.status", "running"),
			),
		}},
	})
}

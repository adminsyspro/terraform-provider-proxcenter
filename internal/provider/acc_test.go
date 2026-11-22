package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// accEnv are the settings of an acceptance run against a real ProxCenter.
var accEnv = []string{
	"PROXCENTER_ENDPOINT", "PROXCENTER_TOKEN", "PROXCENTER_TEST_CONNECTION", "PROXCENTER_TEST_NODE",
	"PROXCENTER_TEST_STORAGE", "PROXCENTER_TEST_IMAGE", "PROXCENTER_TEST_BRIDGE",
}

func accPreCheck(t *testing.T) {
	t.Helper()
	for _, k := range accEnv {
		if os.Getenv(k) == "" {
			t.Fatalf("%s must be set for acceptance tests", k)
		}
	}
}

func accVM(cores, sizeGB int) string {
	return fmt.Sprintf(`
resource "proxcenter_vm" "acc" {
  connection_id = %q
  target_node   = %q
  name          = "tf-acc-catalogue"

  source { image = %q }

  cores     = %d
  memory_mb = 512

  disk {
    slot    = "scsi0"
    storage = %q
    size_gb = %d
  }

  network {
    bridge = %q
  }

  cloud_init {
    user = "debian"
  }

  power_state = "stopped"
}`, os.Getenv("PROXCENTER_TEST_CONNECTION"), os.Getenv("PROXCENTER_TEST_NODE"), os.Getenv("PROXCENTER_TEST_IMAGE"),
		cores, os.Getenv("PROXCENTER_TEST_STORAGE"), sizeGB, os.Getenv("PROXCENTER_TEST_BRIDGE"))
}

// TestAccVMCatalogueLifecycle creates a stopped VM from the catalogue, grows
// its disk, changes its cores and destroys it.
func TestAccVMCatalogueLifecycle(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck: func() { accPreCheck(t) },
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"proxcenter": providerserver.NewProtocol6WithError(New("acc")()),
		},
		Steps: []resource.TestStep{
			{
				Config: accVM(1, 4),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("proxcenter_vm.acc", "vmid"),
					resource.TestCheckResourceAttr("proxcenter_vm.acc", "disk.0.size_gb", "4"),
					resource.TestCheckResourceAttrSet("proxcenter_vm.acc", "network.0.mac_address"),
				),
			},
			{
				Config: accVM(2, 6),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("proxcenter_vm.acc", "cores", "2"),
					resource.TestCheckResourceAttr("proxcenter_vm.acc", "disk.0.size_gb", "6"),
				),
			},
		},
	})
}

package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// accCT is one container from PROXCENTER_TEST_CT_TEMPLATE.
func accCT(res, hostname string, memory, rootGB int, power string) string {
	return fmt.Sprintf(`
resource "proxcenter_lxc" %q {
  connection_id = %q
  target_node   = %q
  hostname      = %q

  source { ostemplate = %q }

  cores     = 1
  memory_mb = %d

  rootfs {
    storage = %q
    size_gb = %d
  }

  network {
    bridge = %q
    ip     = "dhcp"
  }

  power_state = %q
}
`, res, os.Getenv("PROXCENTER_TEST_CONNECTION"), os.Getenv("PROXCENTER_TEST_NODE"), hostname, os.Getenv("PROXCENTER_TEST_CT_TEMPLATE"),
		memory, os.Getenv("PROXCENTER_TEST_STORAGE"), rootGB, os.Getenv("PROXCENTER_TEST_BRIDGE"), power)
}

func accCTPreCheck(t *testing.T) {
	t.Helper()
	accPreCheck(t)
	if os.Getenv("PROXCENTER_TEST_CT_TEMPLATE") == "" {
		t.Skip("PROXCENTER_TEST_CT_TEMPLATE runs the container scenarios")
	}
}

// TestAccLXCLifecycle creates a stopped container from a template, grows its
// root filesystem and memory, starts it, then destroys it.
func TestAccLXCLifecycle(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accCTPreCheck(t) },
		ProtoV6ProviderFactories: accFactories(),
		Steps: []resource.TestStep{
			{Config: accCT("ct", "tf-acc-ct-life", 256, 2, "stopped"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("proxcenter_lxc.ct", "vmid"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "rootfs.size_gb", "2"),
					resource.TestCheckResourceAttrSet("proxcenter_lxc.ct", "network.0.mac_address"))},
			{Config: accCT("ct", "tf-acc-ct-life", 512, 3, "running"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "memory_mb", "512"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "rootfs.size_gb", "3"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "power_state", "running"))},
			{Config: accCT("ct", "tf-acc-ct-life", 512, 3, "running"), PlanOnly: true},
		},
	})
}

// TestAccLXCCloneAndImport clones a container made from the template, then
// imports the clone and plans nothing.
func TestAccLXCCloneAndImport(t *testing.T) {
	cfg := accCT("src", "tf-acc-ct-src", 256, 2, "stopped") + fmt.Sprintf(`
resource "proxcenter_lxc" "clone" {
  connection_id = %q
  hostname      = "tf-acc-ct-clone"

  source { clone_of = proxcenter_lxc.src.vmid }

  cores     = 1
  memory_mb = 384

  rootfs {
    storage = %q
    size_gb = 3
  }

  network {
    bridge = %q
    ip     = "dhcp"
  }

  power_state = "stopped"
}
`, os.Getenv("PROXCENTER_TEST_CONNECTION"), os.Getenv("PROXCENTER_TEST_STORAGE"), os.Getenv("PROXCENTER_TEST_BRIDGE"))
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accCTPreCheck(t) },
		ProtoV6ProviderFactories: accFactories(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.TestCheckResourceAttr("proxcenter_lxc.clone", "rootfs.size_gb", "3")},
			{Config: cfg, ResourceName: "proxcenter_lxc.clone", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"source", "source.%", "source.clone_of", "target_node", "timeouts", "timeouts.%", "password_wo", "ssh_keys", "ssh_keys.#"}},
			{Config: cfg, PlanOnly: true},
		},
	})
}

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/adminsyspro/terraform-provider-proxcenter/internal/client"
)

// The lab acceptance matrix (design step 4). Each test runs once per token
// type: PROXCENTER_TOKEN is a vDC tenant token or a provider token; the
// optional PROXCENTER_TEST_NODE2 enables the DRS case, and
// PROXCENTER_TEST_QUOTA=1 the quota refusal (vDC tenant only).

func accFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){"proxcenter": providerserver.NewProtocol6WithError(New("acc")())}
}

func accClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(client.Config{Endpoint: os.Getenv("PROXCENTER_ENDPOINT"), Token: os.Getenv("PROXCENTER_TOKEN"), UserAgent: "terraform-provider-proxcenter/acc"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// accHCL is one catalogue VM; extra goes inside the resource block.
func accHCL(res, name string, cores, memory int, power, extra string) string {
	return fmt.Sprintf(`
resource "proxcenter_vm" %q {
  connection_id = %q
  target_node   = %q
  name          = %q

  source { image = %q }

  cores     = %d
  memory_mb = %d

  disk {
    slot    = "scsi0"
    storage = %q
    size_gb = 4
  }

  network {
    bridge = %q
  }

  cloud_init {
    user = "debian"
  }

  power_state = %q
%s
}
`, res, os.Getenv("PROXCENTER_TEST_CONNECTION"), os.Getenv("PROXCENTER_TEST_NODE"), name, os.Getenv("PROXCENTER_TEST_IMAGE"),
		cores, memory, os.Getenv("PROXCENTER_TEST_STORAGE"), os.Getenv("PROXCENTER_TEST_BRIDGE"), power, extra)
}

func stateVMID(s *terraform.State, res string) (string, int, error) {
	rs, ok := s.RootModule().Resources[res]
	if !ok {
		return "", 0, fmt.Errorf("%s not in state", res)
	}
	vmid, err := strconv.Atoi(rs.Primary.Attributes["vmid"])
	return rs.Primary.Attributes["node"], vmid, err
}

// captured holds what a Check step saw, for a later PreConfig.
type captured struct {
	node string
	vmid int
}

func capture(res string, into *captured) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		node, vmid, err := stateVMID(s, res)
		into.node, into.vmid = node, vmid
		return err
	}
}

func TestAccVMCloneAndImport(t *testing.T) {
	cfg := accHCL("src", "tf-acc-clone-src", 1, 512, "stopped", "") + fmt.Sprintf(`
resource "proxcenter_vm" "clone" {
  connection_id = %q
  name          = "tf-acc-clone"

  source { clone_of = proxcenter_vm.src.vmid }

  cores     = 2
  memory_mb = 768

  disk {
    slot    = "scsi0"
    storage = %q
    size_gb = 6
  }

  network {
    bridge = %q
  }

  cloud_init {
    user = "ops"
  }

  power_state = "stopped"
}
`, os.Getenv("PROXCENTER_TEST_CONNECTION"), os.Getenv("PROXCENTER_TEST_STORAGE"), os.Getenv("PROXCENTER_TEST_BRIDGE"))
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: accFactories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("proxcenter_vm.clone", "cores", "2"),
					resource.TestCheckResourceAttr("proxcenter_vm.clone", "disk.0.size_gb", "6"),
					resource.TestCheckResourceAttr("proxcenter_vm.clone", "cloud_init.user", "ops"),
				),
			},
			{
				Config: cfg, ResourceName: "proxcenter_vm.clone", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"source", "source.%", "source.clone_of", "target_node", "timeouts", "timeouts.%", "cloud_init.password_wo", "cloud_init.ip_address"},
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}

func TestAccVMDriftAndDRS(t *testing.T) {
	var vm captured
	cfg := accHCL("drift", "tf-acc-drift", 1, 512, "stopped", "")
	steps := []resource.TestStep{
		{Config: cfg, Check: capture("proxcenter_vm.drift", &vm)},
		{
			// Someone changes the cores in the UI: the plan shows it.
			PreConfig: func() {
				if err := accClient(t).UpdateConfig(context.Background(), os.Getenv("PROXCENTER_TEST_CONNECTION"), vm.node, client.GuestQEMU, vm.vmid, map[string]any{"cores": 3}); err != nil {
					t.Fatal(err)
				}
			},
			Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true,
		},
		{Config: cfg, Check: resource.TestCheckResourceAttr("proxcenter_vm.drift", "cores", "1")},
	}
	if node2 := os.Getenv("PROXCENTER_TEST_NODE2"); node2 != "" {
		steps = append(steps,
			resource.TestStep{
				// DRS moves the VM: nothing to plan, the node follows.
				PreConfig: func() { accMigrate(t, vm, node2) },
				Config:    cfg, PlanOnly: true,
			},
			resource.TestStep{Config: cfg, Check: resource.TestCheckResourceAttr("proxcenter_vm.drift", "node", node2)},
		)
	}
	resource.Test(t, resource.TestCase{PreCheck: func() { accPreCheck(t) }, ProtoV6ProviderFactories: accFactories(), Steps: steps})
}

// accMigrate migrates the VM offline through the API, as DRS would, and
// waits for the task. Migration is a provider or MSP operation: a vDC tenant
// run passes a provider token in PROXCENTER_TEST_MIGRATE_TOKEN.
func accMigrate(t *testing.T, vm captured, target string) {
	t.Helper()
	conn := os.Getenv("PROXCENTER_TEST_CONNECTION")
	body, _ := json.Marshal(map[string]any{"target": target, "online": false})
	url := fmt.Sprintf("%s/api/v1/connections/%s/guests/qemu/%s/%d/migrate", os.Getenv("PROXCENTER_ENDPOINT"), conn, vm.node, vm.vmid)
	// The endpoint comes from the operator's environment, as for the provider.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body)) //nolint:gosec // trusted test endpoint
	token := os.Getenv("PROXCENTER_TEST_MIGRATE_TOKEN")
	if token == "" {
		token = os.Getenv("PROXCENTER_TOKEN")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req) //nolint:gosec // trusted test endpoint
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out struct {
		Data  string `json:"data"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("migrate: %d %s", res.StatusCode, out.Error)
	}
	migrator, err := client.New(client.Config{Endpoint: os.Getenv("PROXCENTER_ENDPOINT"), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.WaitTask(context.Background(), conn, vm.node, out.Data); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Second) // /cluster/resources catches up
}

func TestAccVMRunningUpdateReboots(t *testing.T) {
	// A freshly booted cloud image ignores ACPI for a while: force_stop lets
	// the reboot fall back to a hard stop and start.
	extra := "  reboot_on_change = true\n  force_stop       = true"
	first := accHCL("hot", "tf-acc-hot", 1, 512, "running", extra)
	second := accHCL("hot", "tf-acc-hot", 2, 1024, "running", extra)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: accFactories(),
		Steps: []resource.TestStep{
			{Config: first},
			{Config: second, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("proxcenter_vm.hot", plancheck.ResourceActionUpdate)}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("proxcenter_vm.hot", "cores", "2"),
					resource.TestCheckResourceAttr("proxcenter_vm.hot", "memory_mb", "1024"))},
			{Config: second, PlanOnly: true},
		},
	})
}

func TestAccVMQuotaRefusal(t *testing.T) {
	if os.Getenv("PROXCENTER_TEST_QUOTA") != "1" {
		t.Skip("PROXCENTER_TEST_QUOTA=1 runs the quota refusal (vDC tenant token)")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: accFactories(),
		Steps: []resource.TestStep{{
			Config:      accHCL("big", "tf-acc-quota", 1, 262144, "stopped", ""),
			ExpectError: regexp.MustCompile(`(?i)quota`),
		}},
	})
}

func TestAccVMDeletedOutsideIsRecreated(t *testing.T) {
	var vm captured
	cfg := accHCL("gone", "tf-acc-gone", 1, 512, "stopped", "")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: accFactories(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: capture("proxcenter_vm.gone", &vm)},
			{
				PreConfig: func() {
					if err := accClient(t).DeleteVM(context.Background(), os.Getenv("PROXCENTER_TEST_CONNECTION"), vm.node, client.GuestQEMU, vm.vmid); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("proxcenter_vm.gone", plancheck.ResourceActionCreate)}}},
		},
	})
}

package provider

import (
	"regexp"
	"strings"
	"testing"
)

// ctHCL is a proxcenter_lxc with the given source and target_node lines.
func ctHCL(source, targetNode, extra string) string {
	return `
resource "proxcenter_lxc" "ct" {
  connection_id = "conn1"
  hostname      = "ct-web"
` + targetNode + `
` + source + `
  cores     = 2
  memory_mb = 1024

  rootfs {
    storage = "ceph-gold"
    size_gb = 8
  }

  network {
    bridge = "vmbr0"
    ip     = "dhcp"
  }
` + extra + `
  power_state = "stopped"
}`
}

const ctTemplate = `  source { ostemplate = "local:vztmpl/alpine-3.24-default_20261105_amd64.tar.xz" }`

func TestLXCPlanCreateNeedsSource(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), ctHCL("", `  target_node = "pve1"`, ""), regexp.MustCompile(`(?s)source.*required to create`))
}

func TestLXCPlanTemplateNeedsTargetNode(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), ctHCL(ctTemplate, "", ""), regexp.MustCompile(`(?s)target_node.*nodes`))
}

func TestLXCPlanTemplateWithTargetNodeOK(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), ctHCL(ctTemplate, `  target_node = "pve1"`, ""), nil)
}

func TestLXCPlanCloneWithoutTargetNodeOK(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), ctHCL(`  source { clone_of = 9001 }`, "", ""), nil)
}

func TestLXCPlanSourceExactlyOne(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), ctHCL(`  source {
    ostemplate = "local:vztmpl/a.tar.xz"
    clone_of   = 9001
  }`, `  target_node = "pve1"`, ""), regexp.MustCompile(`exactly one of ostemplate or clone_of`))
}

func TestLXCPlanNeedsRootfs(t *testing.T) {
	hcl := regexp.MustCompile(`(?s)  rootfs \{.*?\n  \}\n`).ReplaceAllString(ctHCL(ctTemplate, `  target_node = "pve1"`, ""), "")
	planOnly(t, newFakeAPI(t).withVDC(), hcl, regexp.MustCompile(`(?i)rootfs`))
}

func TestLXCPlanSeveralNICsNeedSlots(t *testing.T) {
	hcl := ctHCL(ctTemplate, `  target_node = "pve1"`, "  network {\n    bridge = \"vmbr1\"\n  }\n")
	planOnly(t, newFakeAPI(t).withVDC(), hcl, regexp.MustCompile(`(?s)slot.*several\s+network`))
}

func TestLXCPlanMountPointSlotIsMpN(t *testing.T) {
	hcl := ctHCL(ctTemplate, `  target_node = "pve1"`, "  mount_point {\n    slot    = \"data\"\n    storage = \"ceph-gold\"\n    size_gb = 2\n    path    = \"/data\"\n  }\n")
	planOnly(t, newFakeAPI(t).withVDC(), hcl, regexp.MustCompile(`mp0`))
	if !strings.Contains(hcl, "mount_point") {
		t.Fatal("bad fixture")
	}
}

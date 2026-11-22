package vmconfig

import "testing"

func vlan(n int64) *int64 { return &n }
func flag(b bool) *bool   { return &b }

func TestRenderNewDisk(t *testing.T) {
	if got := RenderDisk(Disk{"scsi1", "ceph-gold", 20}); got != "ceph-gold:20" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderNICPreservesUnknownSubkeys(t *testing.T) {
	cur := "virtio=BC:24:11:AA:BB:CC,bridge=vnet-prod,tag=120,firewall=1,queues=4"
	got := RenderNIC(NIC{Slot: "net0", Model: "virtio", Bridge: "vnet-prod", VLAN: vlan(130)}, cur)
	if got != "virtio=BC:24:11:AA:BB:CC,bridge=vnet-prod,tag=130,firewall=1,queues=4" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderNICRemovesTag(t *testing.T) {
	got := RenderNIC(NIC{Slot: "net0", Model: "virtio", Bridge: "vmbr0"}, "virtio=BC:24:11:AA:BB:CC,bridge=vnet-prod,tag=120")
	if got != "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderNICModelAndMACChange(t *testing.T) {
	got := RenderNIC(NIC{Slot: "net0", Model: "e1000", MAC: "BC:24:11:00:00:01", Bridge: "vmbr0", Firewall: flag(false)}, "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0,firewall=1")
	if got != "e1000=BC:24:11:00:00:01,bridge=vmbr0,firewall=0" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderNewNIC(t *testing.T) {
	if got := RenderNIC(NIC{Slot: "net1", Model: "virtio", Bridge: "vnet2", VLAN: vlan(7), Firewall: flag(true)}, ""); got != "virtio,bridge=vnet2,tag=7,firewall=1" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderIPConfig(t *testing.T) {
	for _, c := range []struct{ ip, gw, want string }{{"dhcp", "", "ip=dhcp"}, {"10.0.0.5/24", "10.0.0.1", "ip=10.0.0.5/24,gw=10.0.0.1"}, {"", "", ""}} {
		if got := RenderIPConfig(c.ip, c.gw); got != c.want {
			t.Errorf("RenderIPConfig(%q,%q) = %q", c.ip, c.gw, got)
		}
	}
}

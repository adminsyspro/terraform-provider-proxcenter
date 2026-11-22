package vmconfig

import (
	"reflect"
	"strings"
	"testing"
)

func baseVM() VM {
	return VM{
		Name: "web-01", Cores: 4, Sockets: 1, MemoryMB: 8192, CPU: "x86-64-v2-AES",
		Disks:     []Disk{{"scsi0", "ceph-gold", 64}},
		NICs:      []NIC{{Slot: "net0", Model: "virtio", MAC: "BC:24:11:AA:BB:CC", Bridge: "vnet-prod", VLAN: vlan(120), Firewall: flag(true)}},
		CloudInit: &CloudInit{User: "ops", SSHKeys: []string{"ssh-ed25519 AAAA ops"}, IP: "dhcp"},
	}
}

var raw = map[string]any{"net0": "virtio=BC:24:11:AA:BB:CC,bridge=vnet-prod,tag=120,firewall=1,queues=4"}

func TestChangesOnlyChangedKeys(t *testing.T) {
	want := baseVM()
	want.Cores = 8
	c, err := ConfigChanges(baseVM(), want, raw)
	if err != nil || !reflect.DeepEqual(c.Keys, map[string]any{"cores": int64(8)}) || len(c.Resizes)+len(c.Moves) != 0 {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestChangesNothing(t *testing.T) {
	c, err := ConfigChanges(baseVM(), baseVM(), raw)
	if err != nil || !c.Empty() {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestChangesGrowDiskIsResizeAbsolute(t *testing.T) {
	want := baseVM()
	want.Disks[0].SizeGB = 80
	c, err := ConfigChanges(baseVM(), want, raw)
	if err != nil || !reflect.DeepEqual(c.Resizes, []Resize{{Disk: "scsi0", Size: "80G"}}) || len(c.Keys) != 0 {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestChangesShrinkIsError(t *testing.T) {
	want := baseVM()
	want.Disks[0].SizeGB = 32
	_, err := ConfigChanges(baseVM(), want, raw)
	if err == nil || !strings.Contains(err.Error(), "64 GB to 32 GB") || !strings.Contains(err.Error(), "cannot shrink") {
		t.Fatalf("got %v", err)
	}
}

func TestChangesRemovedDiskIsError(t *testing.T) {
	old := baseVM()
	old.Disks = append(old.Disks, Disk{"scsi1", "ceph-gold", 20})
	_, err := ConfigChanges(old, baseVM(), raw)
	if err == nil || !strings.Contains(err.Error(), "scsi1") || !strings.Contains(err.Error(), "does not detach disks") {
		t.Fatalf("got %v", err)
	}
}

func TestChangesStorageIsMove(t *testing.T) {
	want := baseVM()
	want.Disks[0].Storage = "ceph-silver"
	c, err := ConfigChanges(baseVM(), want, raw)
	if err != nil || !reflect.DeepEqual(c.Moves, []Move{{Disk: "scsi0", Storage: "ceph-silver"}}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestChangesNewDiskAndNIC(t *testing.T) {
	want := baseVM()
	want.Disks = append(want.Disks, Disk{"scsi1", "ceph-gold", 20})
	want.NICs = append(want.NICs, NIC{Slot: "net1", Model: "virtio", Bridge: "vnet2"})
	c, err := ConfigChanges(baseVM(), want, raw)
	if err != nil || c.Keys["scsi1"] != "ceph-gold:20" || c.Keys["net1"] != "virtio,bridge=vnet2" || len(c.Keys) != 2 {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestChangesVLANKeepsMACAndSubkeys(t *testing.T) {
	want := baseVM()
	want.NICs[0].VLAN = vlan(130)
	want.NICs[0].MAC = "" // not described: keep the current one
	c, err := ConfigChanges(baseVM(), want, raw)
	if err != nil || c.Keys["net0"] != "virtio=BC:24:11:AA:BB:CC,bridge=vnet-prod,tag=130,firewall=1,queues=4" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestChangesRemovedNICIsDeleted(t *testing.T) {
	old := baseVM()
	old.NICs = append(old.NICs, NIC{Slot: "net1", Model: "virtio", Bridge: "vnet2"})
	c, err := ConfigChanges(old, baseVM(), raw)
	if err != nil || c.Keys["delete"] != "net1" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestChangesCloudInit(t *testing.T) {
	want := baseVM()
	want.CloudInit = &CloudInit{User: "admin", SSHKeys: []string{"ssh-ed25519 AAAA ops", "ssh-rsa BBBB ci"}, IP: "10.15.15.30/24", Gateway: "10.15.15.1", Nameserver: "1.1.1.1"}
	c, err := ConfigChanges(baseVM(), want, raw)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := map[string]any{"ciuser": "admin", "sshkeys": "ssh-ed25519 AAAA ops\nssh-rsa BBBB ci", "ipconfig0": "ip=10.15.15.30/24,gw=10.15.15.1", "nameserver": "1.1.1.1"}
	if !reflect.DeepEqual(c.Keys, wantKeys) {
		t.Fatalf("got %v", c.Keys)
	}
}

func TestChangesUnsetIPLeavesIPConfig(t *testing.T) {
	want := baseVM()
	want.CloudInit.IP = ""
	c, err := ConfigChanges(baseVM(), want, raw)
	if err != nil || !c.Empty() {
		t.Fatalf("an unset ip must leave ipconfig0 to the API: %+v, %v", c, err)
	}
}

func TestChangesUnsetCPUKeepsCurrent(t *testing.T) {
	want := baseVM()
	want.CPU = ""
	if c, err := ConfigChanges(baseVM(), want, raw); err != nil || !c.Empty() {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestNeedsReboot(t *testing.T) {
	if NeedsReboot(nil) || NeedsReboot(map[string]any{}) || !NeedsReboot(map[string]any{"cores": "8"}) {
		t.Fatal("NeedsReboot wrong")
	}
}

func TestChangesUndescribedCloudInitFieldsKept(t *testing.T) {
	old := baseVM()
	old.CloudInit.Nameserver = "9.9.9.9"
	want := baseVM()
	want.CloudInit = &CloudInit{}
	if c, err := ConfigChanges(old, want, raw); err != nil || !c.Empty() {
		t.Fatalf("unset cloud-init fields must keep the current values: %+v, %v", c, err)
	}
}

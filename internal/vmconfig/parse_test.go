package vmconfig

import (
	"reflect"
	"testing"
)

// labConfig is a config read on the lab bench, trimmed.
func labConfig() map[string]any {
	return map[string]any{
		"name":      "web-01",
		"cores":     float64(4),
		"sockets":   "1",
		"memory":    "8192",
		"cpu":       "x86-64-v2-AES",
		"scsi0":     "ceph-gold:vm-120-disk-0,iothread=1,size=64G",
		"scsi1":     "ceph-gold:vm-120-disk-1,size=512M",
		"virtio2":   "ceph-silver:vm-120-disk-2,size=1T",
		"ide2":      "ceph-gold:vm-120-cloudinit,media=cdrom",
		"ide0":      "local:iso/debian.iso,media=cdrom",
		"net0":      "virtio=BC:24:11:AA:BB:CC,bridge=vnet-prod,tag=120,firewall=1,queues=4",
		"net1":      "e1000=BC:24:11:AA:BB:DD,bridge=vmbr0",
		"ciuser":    "ops",
		"sshkeys":   "ssh-ed25519%20AAAA%2Bx%20ops%40host%0Assh-rsa%20BBBB%20ci%0A",
		"ipconfig0": "ip=10.15.15.20/24,gw=10.15.15.1",
		"digest":    "abc",
	}
}

func TestParseScalars(t *testing.T) {
	vm, err := Parse(labConfig())
	if err != nil {
		t.Fatal(err)
	}
	if vm.Name != "web-01" || vm.Cores != 4 || vm.Sockets != 1 || vm.MemoryMB != 8192 || vm.CPU != "x86-64-v2-AES" {
		t.Fatalf("got %+v", vm)
	}
}

func TestParseDisksSkipsCdromAndCloudinit(t *testing.T) {
	vm, _ := Parse(labConfig())
	want := []Disk{{"scsi0", "ceph-gold", 64}, {"scsi1", "ceph-gold", 1}, {"virtio2", "ceph-silver", 1024}}
	if !reflect.DeepEqual(vm.Disks, want) {
		t.Fatalf("disks = %+v", vm.Disks)
	}
}

func TestParseSizeUnits(t *testing.T) {
	for in, want := range map[string]int64{"64G": 64, "512M": 1, "1T": 1024, "1536M": 2, "34359738368": 32, "10485760K": 10} {
		if got := parseSizeGB(in); got != want {
			t.Errorf("parseSizeGB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseNIC(t *testing.T) {
	vm, _ := Parse(labConfig())
	if len(vm.NICs) != 2 {
		t.Fatalf("nics = %+v", vm.NICs)
	}
	n := vm.NICs[0]
	if n.Slot != "net0" || n.Model != "virtio" || n.MAC != "BC:24:11:AA:BB:CC" || n.Bridge != "vnet-prod" || n.VLAN == nil || *n.VLAN != 120 || n.Firewall == nil || !*n.Firewall {
		t.Fatalf("net0 = %+v", n)
	}
	if m := vm.NICs[1]; m.Model != "e1000" || m.VLAN != nil || m.Firewall == nil || *m.Firewall {
		t.Fatalf("net1 = %+v", m)
	}
}

func TestParseSSHKeysDecoded(t *testing.T) {
	vm, _ := Parse(labConfig())
	want := []string{"ssh-ed25519 AAAA+x ops@host", "ssh-rsa BBBB ci"}
	if vm.CloudInit == nil || !reflect.DeepEqual(vm.CloudInit.SSHKeys, want) || vm.CloudInit.User != "ops" {
		t.Fatalf("cloud-init = %+v", vm.CloudInit)
	}
}

func TestParseIPConfig(t *testing.T) {
	vm, _ := Parse(labConfig())
	if vm.CloudInit.IP != "10.15.15.20/24" || vm.CloudInit.Gateway != "10.15.15.1" {
		t.Fatalf("got %+v", vm.CloudInit)
	}
	cfg := labConfig()
	cfg["ipconfig0"] = "ip=dhcp"
	vm, _ = Parse(cfg)
	if vm.CloudInit.IP != "dhcp" || vm.CloudInit.Gateway != "" {
		t.Fatalf("got %+v", vm.CloudInit)
	}
}

func TestParseWithoutCloudInit(t *testing.T) {
	vm, err := Parse(map[string]any{"name": "x", "cores": "2", "memory": 2048, "scsi0": "local-lvm:vm-1-disk-0,size=8G"})
	if err != nil || vm.CloudInit != nil || vm.Sockets != 1 {
		t.Fatalf("got %+v, %v", vm, err)
	}
}

func TestParseRejectsBadNumber(t *testing.T) {
	if _, err := Parse(map[string]any{"cores": "four"}); err == nil {
		t.Fatal("bad cores accepted")
	}
}

func TestParseMemoryProperty(t *testing.T) {
	for in, want := range map[string]int64{"8192,max=32768": 8192, "current=4096,max=8192": 4096, "2048": 2048} {
		vm, err := Parse(map[string]any{"memory": in})
		if err != nil || vm.MemoryMB != want {
			t.Errorf("memory %q: got %d, %v", in, vm.MemoryMB, err)
		}
	}
}

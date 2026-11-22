package vmconfig

import (
	"reflect"
	"strings"
	"testing"
)

func labCT() map[string]any {
	return map[string]any{
		"hostname":     "ct-web",
		"cores":        float64(2),
		"memory":       float64(1024),
		"swap":         "512",
		"unprivileged": float64(1),
		"features":     "nesting=1,keyctl=1",
		"rootfs":       "CephStoragePool:vm-130-disk-0,size=8G",
		"mp0":          "CephStoragePool:vm-130-disk-1,mp=/data,backup=1,size=10G",
		"mp1":          "CephStoragePool:vm-130-disk-2,mp=/logs,size=2G",
		"net0":         "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:AA:00:01,ip=dhcp,ip6=auto,tag=150,type=veth",
		"net1":         "name=eth1,bridge=vnet-prod,hwaddr=BC:24:11:AA:00:02,ip=10.15.15.30/24,gw=10.15.15.1,firewall=1,type=veth",
		"nameserver":   "1.1.1.1",
		"searchdomain": "lab.local",
		"ostype":       "debian",
	}
}

func TestParseCT(t *testing.T) {
	ct, err := ParseCT(labCT())
	if err != nil {
		t.Fatal(err)
	}
	if ct.Hostname != "ct-web" || ct.Cores != 2 || ct.MemoryMB != 1024 || ct.SwapMB == nil || *ct.SwapMB != 512 || !ct.Unprivileged {
		t.Fatalf("scalars %+v", ct)
	}
	if !reflect.DeepEqual(ct.Features, map[string]bool{"nesting": true, "keyctl": true}) {
		t.Fatalf("features %v", ct.Features)
	}
	if ct.RootFS != (MountPoint{"rootfs", "CephStoragePool", "", 8}) {
		t.Fatalf("rootfs %+v", ct.RootFS)
	}
	want := []MountPoint{{"mp0", "CephStoragePool", "/data", 10}, {"mp1", "CephStoragePool", "/logs", 2}}
	if !reflect.DeepEqual(ct.MountPoints, want) {
		t.Fatalf("mount points %+v", ct.MountPoints)
	}
	n0, n1 := ct.NICs[0], ct.NICs[1]
	if n0.Slot != "net0" || n0.Name != "eth0" || n0.Bridge != "vmbr0" || n0.MAC != "BC:24:11:AA:00:01" || n0.IP != "dhcp" || n0.VLAN == nil || *n0.VLAN != 150 || *n0.Firewall {
		t.Fatalf("net0 %+v", n0)
	}
	if n1.IP != "10.15.15.30/24" || n1.Gateway != "10.15.15.1" || !*n1.Firewall || n1.VLAN != nil {
		t.Fatalf("net1 %+v", n1)
	}
	if ct.Nameserver != "1.1.1.1" || ct.SearchDomain != "lab.local" {
		t.Fatalf("dns %+v", ct)
	}
}

func TestParseCTPrivilegedAndBare(t *testing.T) {
	ct, err := ParseCT(map[string]any{"hostname": "x", "cores": "1", "memory": "512", "rootfs": "local-lvm:vm-1-disk-0,size=4G"})
	if err != nil || ct.Unprivileged || ct.Features != nil || ct.SwapMB != nil {
		t.Fatalf("got %+v, %v", ct, err)
	}
}

func TestRenderCTNICKeepsUnknownSubkeys(t *testing.T) {
	cur := "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:AA:00:01,ip=dhcp,ip6=auto,tag=150,type=veth"
	got := RenderCTNIC(CTNIC{Slot: "net0", Name: "eth0", Bridge: "vmbr0", IP: "dhcp", VLAN: vlan(160)}, cur)
	if got != "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:AA:00:01,ip=dhcp,ip6=auto,tag=160,type=veth" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderNewCTNIC(t *testing.T) {
	got := RenderCTNIC(CTNIC{Slot: "net1", Name: "eth1", Bridge: "vnet2", IP: "10.0.0.5/24", Gateway: "10.0.0.1", Firewall: flag(true)}, "")
	if got != "name=eth1,bridge=vnet2,ip=10.0.0.5/24,gw=10.0.0.1,firewall=1" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderFeatures(t *testing.T) {
	if got := RenderFeatures(map[string]bool{"nesting": true, "keyctl": false, "fuse": true}, ""); got != "fuse=1,keyctl=0,nesting=1" {
		t.Fatalf("got %q", got)
	}
}

func baseCT(t *testing.T) CT {
	t.Helper()
	ct, err := ParseCT(labCT())
	if err != nil {
		t.Fatal(err)
	}
	return ct
}

func TestCTChangesOnlyChangedKeys(t *testing.T) {
	want := baseCT(t)
	want.MemoryMB, want.Cores = 2048, 4
	c, err := CTChanges(baseCT(t), want, labCT())
	if err != nil || !reflect.DeepEqual(c.Keys, map[string]any{"memory": int64(2048), "cores": int64(4)}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestCTChangesNothing(t *testing.T) {
	if c, err := CTChanges(baseCT(t), baseCT(t), labCT()); err != nil || !c.Empty() {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestCTChangesGrowAndMove(t *testing.T) {
	want := baseCT(t)
	want.RootFS.SizeGB = 16
	want.MountPoints[0].Storage = "ceph-silver"
	want.MountPoints[1].SizeGB = 5
	c, err := CTChanges(baseCT(t), want, labCT())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Resizes, []Resize{{"rootfs", "16G"}, {"mp1", "5G"}}) || !reflect.DeepEqual(c.Moves, []Move{{"mp0", "ceph-silver"}}) {
		t.Fatalf("got %+v", c)
	}
}

func TestCTChangesShrinkIsError(t *testing.T) {
	want := baseCT(t)
	want.RootFS.SizeGB = 4
	if _, err := CTChanges(baseCT(t), want, labCT()); err == nil || !strings.Contains(err.Error(), "rootfs: size 8 GB to 4 GB") {
		t.Fatalf("got %v", err)
	}
}

func TestCTChangesRemovedMountPointIsError(t *testing.T) {
	want := baseCT(t)
	want.MountPoints = want.MountPoints[:1]
	if _, err := CTChanges(baseCT(t), want, labCT()); err == nil || !strings.Contains(err.Error(), "mp1 is no longer described") {
		t.Fatalf("got %v", err)
	}
}

func TestCTChangesAddedMountPointIsError(t *testing.T) {
	want := baseCT(t)
	want.MountPoints = append(want.MountPoints, MountPoint{"mp2", "CephStoragePool", "/cache", 3})
	if _, err := CTChanges(baseCT(t), want, labCT()); err == nil || !strings.Contains(err.Error(), "mp2") || !strings.Contains(err.Error(), "at creation only") {
		t.Fatalf("got %v", err)
	}
}

func TestCTChangesRemovedNICIsError(t *testing.T) {
	want := baseCT(t)
	want.NICs = want.NICs[:1]
	if _, err := CTChanges(baseCT(t), want, labCT()); err == nil || !strings.Contains(err.Error(), "net1 is no longer described") {
		t.Fatalf("got %v", err)
	}
}

func TestCTChangesNICVLANAndNew(t *testing.T) {
	want := baseCT(t)
	want.NICs[0].VLAN = vlan(160)
	want.NICs[0].MAC = ""
	want.NICs = append(want.NICs, CTNIC{Slot: "net2", Name: "eth2", Bridge: "vmbr1", IP: "dhcp"})
	c, err := CTChanges(baseCT(t), want, labCT())
	if err != nil {
		t.Fatal(err)
	}
	if c.Keys["net0"] != "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:AA:00:01,ip=dhcp,ip6=auto,tag=160,type=veth" || c.Keys["net2"] != "name=eth2,bridge=vmbr1,ip=dhcp" || len(c.Keys) != 2 {
		t.Fatalf("got %v", c.Keys)
	}
}

func TestCTChangesFeatures(t *testing.T) {
	want := baseCT(t)
	want.Features = map[string]bool{"nesting": true}
	c, err := CTChanges(baseCT(t), want, labCT())
	if err != nil || c.Keys["features"] != "nesting=1" {
		t.Fatalf("got %+v, %v", c, err)
	}
	want.Features = nil // not described: keep
	if c, err := CTChanges(baseCT(t), want, labCT()); err != nil || !c.Empty() {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestCTChangesIgnoreCreationOnlyKeys(t *testing.T) {
	want := baseCT(t)
	want.Nameserver, want.SearchDomain, want.Unprivileged = "9.9.9.9", "other", false
	if c, err := CTChanges(baseCT(t), want, labCT()); err != nil || !c.Empty() {
		t.Fatalf("creation-only keys must not be written: %+v, %v", c, err)
	}
}

func TestRenderFeaturesKeepsValuedKeys(t *testing.T) {
	if got := RenderFeatures(map[string]bool{"nesting": false}, "mount=nfs;cifs,nesting=1,keyctl=1"); got != "mount=nfs;cifs,nesting=0" {
		t.Fatalf("got %q", got)
	}
}

func TestParseCTFeaturesSkipValuedKeys(t *testing.T) {
	ct, _ := ParseCT(map[string]any{"features": "mount=nfs;cifs,nesting=1"})
	if len(ct.Features) != 1 || !ct.Features["nesting"] {
		t.Fatalf("features %v", ct.Features)
	}
}

func TestCTChangesSwapZero(t *testing.T) {
	want := baseCT(t)
	zero := int64(0)
	want.SwapMB = &zero
	c, err := CTChanges(baseCT(t), want, labCT())
	if err != nil || c.Keys["swap"] != int64(0) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestCTChangesMountPointPathIsError(t *testing.T) {
	want := baseCT(t)
	want.MountPoints[0].Path = "/srv"
	if _, err := CTChanges(baseCT(t), want, labCT()); err == nil || !strings.Contains(err.Error(), "path of mp0") {
		t.Fatalf("got %v", err)
	}
}

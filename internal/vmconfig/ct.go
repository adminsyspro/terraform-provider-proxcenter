package vmconfig

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// MountPoint is the root filesystem (Slot "rootfs") or a mount point (mpN)
// of a container.
type MountPoint struct {
	Slot    string
	Storage string
	Path    string
	SizeGB  int64
}

// CTNIC is a container network card. Empty strings and nil pointers mean
// "not described": the current value is kept.
type CTNIC struct {
	Slot     string
	Name     string
	Bridge   string
	MAC      string
	IP       string
	Gateway  string
	VLAN     *int64
	Firewall *bool
}

// CT is the part of a container config the provider describes. Nameserver,
// SearchDomain and Unprivileged are set at creation only.
type CT struct {
	Hostname string
	Cores    int64
	MemoryMB int64
	// SwapMB is nil when unknown or not described: 0 is a real value.
	SwapMB       *int64
	Unprivileged bool
	// Features holds the boolean features only (nesting, keyctl, fuse...);
	// a valued one such as mount=nfs;cifs stays in the raw config.
	Features     map[string]bool
	RootFS       MountPoint
	MountPoints  []MountPoint
	NICs         []CTNIC
	Nameserver   string
	SearchDomain string
}

var mpKeyRe = regexp.MustCompile(`^mp\d+$`)

// ParseCT reads the described part of a Proxmox container config.
func ParseCT(data map[string]any) (CT, error) {
	ct := CT{Hostname: str(data["hostname"]), Nameserver: str(data["nameserver"]), SearchDomain: str(data["searchdomain"])}
	for key, dst := range map[string]*int64{"cores": &ct.Cores, "memory": &ct.MemoryMB} {
		if v, ok := data[key]; ok && v != nil {
			n, err := toInt(v)
			if err != nil {
				return CT{}, fmt.Errorf("config key %s: %w", key, err)
			}
			*dst = n
		}
	}
	if v, ok := data["swap"]; ok && v != nil {
		n, err := toInt(v)
		if err != nil {
			return CT{}, fmt.Errorf("config key swap: %w", err)
		}
		ct.SwapMB = &n
	}
	ct.Unprivileged = str(data["unprivileged"]) == "1" || data["unprivileged"] == true
	if f := str(data["features"]); f != "" {
		ct.Features = map[string]bool{}
		for _, o := range splitOptions(f) {
			if o.value == "0" || o.value == "1" {
				ct.Features[o.key] = o.value == "1"
			}
		}
	}
	if r := str(data["rootfs"]); r != "" {
		ct.RootFS = parseMountPoint("rootfs", r)
	}
	for key, v := range data {
		switch {
		case mpKeyRe.MatchString(key):
			ct.MountPoints = append(ct.MountPoints, parseMountPoint(key, str(v)))
		case nicKeyRe.MatchString(key):
			ct.NICs = append(ct.NICs, parseCTNIC(key, str(v)))
		}
	}
	sort.Slice(ct.MountPoints, func(i, j int) bool { return slotLess(ct.MountPoints[i].Slot, ct.MountPoints[j].Slot) })
	sort.Slice(ct.NICs, func(i, j int) bool { return slotLess(ct.NICs[i].Slot, ct.NICs[j].Slot) })
	return ct, nil
}

func parseMountPoint(slot, value string) MountPoint {
	m := MountPoint{Slot: slot}
	parts := splitOptions(value)
	if len(parts) > 0 {
		if i := strings.Index(parts[0].raw, ":"); i > 0 {
			m.Storage = parts[0].raw[:i]
		}
	}
	for _, p := range parts[1:] {
		switch p.key {
		case "mp":
			m.Path = p.value
		case "size":
			m.SizeGB = parseSizeGB(p.value)
		}
	}
	return m
}

func parseCTNIC(slot, value string) CTNIC {
	n := CTNIC{Slot: slot}
	off := false
	n.Firewall = &off
	for _, p := range splitOptions(value) {
		switch p.key {
		case "name":
			n.Name = p.value
		case "bridge":
			n.Bridge = p.value
		case "hwaddr":
			n.MAC = p.value
		case "ip":
			n.IP = p.value
		case "gw":
			n.Gateway = p.value
		case "tag":
			if v, err := strconv.ParseInt(p.value, 10, 64); err == nil {
				n.VLAN = &v
			}
		case "firewall":
			b := p.value == "1"
			n.Firewall = &b
		}
	}
	return n
}

// RenderMountPoint is the value that creates a mount point (or rootfs). A
// mount point is included in backups: Proxmox leaves a volume mount point
// out of them unless told, and the API cannot change it afterwards.
func RenderMountPoint(m MountPoint) string {
	out := fmt.Sprintf("%s:%d", m.Storage, m.SizeGB)
	if m.Path != "" {
		out += ",mp=" + m.Path + ",backup=1"
	}
	return out
}

// RenderCTNIC is the netN value for n, changing only what n describes in
// current (empty for a new card) and keeping every other sub-key.
func RenderCTNIC(n CTNIC, current string) string {
	set := map[string]string{"name": n.Name, "bridge": n.Bridge, "hwaddr": n.MAC, "ip": n.IP, "gw": n.Gateway}
	if n.VLAN != nil {
		set["tag"] = strconv.FormatInt(*n.VLAN, 10)
	}
	if n.Firewall != nil {
		set["firewall"] = boolFlag(*n.Firewall)
	}
	var parts []string
	seen := map[string]bool{}
	for _, o := range splitOptions(current) {
		switch {
		case o.key == "tag" && n.VLAN == nil:
			continue // no longer described: untagged
		case set[o.key] != "":
			parts = append(parts, o.key+"="+set[o.key])
			seen[o.key] = true
		default:
			parts = append(parts, o.raw)
		}
	}
	for _, k := range []string{"name", "bridge", "hwaddr", "ip", "gw", "tag", "firewall"} {
		// An absent firewall key is off already: only "on" is worth adding.
		if v := set[k]; v != "" && !seen[k] && (k != "firewall" || v == "1") {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

// RenderFeatures is the features value: the described boolean features set
// onto current, every other key of current (mount=nfs;cifs...) kept.
func RenderFeatures(f map[string]bool, current string) string {
	var parts []string
	seen := map[string]bool{}
	for _, o := range splitOptions(current) {
		if v, described := f[o.key]; described {
			parts = append(parts, o.key+"="+boolFlag(v))
			seen[o.key] = true
		} else if o.value != "0" && o.value != "1" {
			parts = append(parts, o.raw) // not a boolean: never ours to drop
		}
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+boolFlag(f[k]))
	}
	return strings.Join(parts, ",")
}

// CTChanges is what turns old into desired. Mount points are set at
// creation only, so a new one is an error, like a removed or shrunk one; a
// removed card is an error too. Nameserver, search domain and privilege are
// creation only and never written here.
func CTChanges(old, desired CT, currentRaw map[string]any) (Changes, error) {
	c := Changes{Keys: map[string]any{}}
	if desired.Hostname != "" && desired.Hostname != old.Hostname {
		c.Keys["hostname"] = desired.Hostname
	}
	for key, pair := range map[string][2]int64{"cores": {old.Cores, desired.Cores}, "memory": {old.MemoryMB, desired.MemoryMB}} {
		if pair[1] != 0 && pair[0] != pair[1] {
			c.Keys[key] = pair[1]
		}
	}
	if desired.SwapMB != nil && (old.SwapMB == nil || *old.SwapMB != *desired.SwapMB) {
		c.Keys["swap"] = *desired.SwapMB
	}
	if desired.Features != nil && !mapsEqual(desired.Features, old.Features) {
		c.Keys["features"] = RenderFeatures(desired.Features, str(currentRaw["features"]))
	}
	if err := mountPointChange(old.RootFS, desired.RootFS, &c); err != nil {
		return Changes{}, err
	}
	for _, o := range old.MountPoints {
		if !slices.ContainsFunc(desired.MountPoints, func(d MountPoint) bool { return d.Slot == o.Slot }) {
			return Changes{}, fmt.Errorf("mount point %s is no longer described: remove it in ProxCenter, the provider does not detach volumes", o.Slot)
		}
	}
	for _, d := range desired.MountPoints {
		i := slices.IndexFunc(old.MountPoints, func(o MountPoint) bool { return o.Slot == d.Slot })
		if i < 0 {
			return Changes{}, fmt.Errorf("mount point %s is new: ProxCenter sets the mount points of a container at creation only; add it in ProxCenter, then describe it", d.Slot)
		}
		if err := mountPointChange(old.MountPoints[i], d, &c); err != nil {
			return Changes{}, err
		}
	}
	for _, o := range old.NICs {
		if !slices.ContainsFunc(desired.NICs, func(n CTNIC) bool { return n.Slot == o.Slot }) {
			return Changes{}, fmt.Errorf("network %s is no longer described: describe it, or remove it in ProxCenter; the provider does not remove network cards", o.Slot)
		}
	}
	for _, n := range desired.NICs {
		i := slices.IndexFunc(old.NICs, func(o CTNIC) bool { return o.Slot == n.Slot })
		if i < 0 {
			c.Keys[n.Slot] = RenderCTNIC(n, "")
			continue
		}
		if ctNICDiffers(old.NICs[i], n) {
			c.Keys[n.Slot] = RenderCTNIC(n, str(currentRaw[n.Slot]))
		}
	}
	return c, nil
}

func mountPointChange(old, d MountPoint, c *Changes) error {
	if d.Path != "" && old.Path != "" && d.Path != old.Path {
		return fmt.Errorf("path of %s: %s to %s, ProxCenter sets the path of a mount point at creation only", d.Slot, old.Path, d.Path)
	}
	if d.SizeGB != 0 && d.SizeGB < old.SizeGB {
		return fmt.Errorf("%s: size %d GB to %d GB, Proxmox cannot shrink a volume", d.Slot, old.SizeGB, d.SizeGB)
	}
	if d.Storage != "" && d.Storage != old.Storage {
		c.Moves = append(c.Moves, Move{Disk: d.Slot, Storage: d.Storage})
	}
	if d.SizeGB > old.SizeGB {
		c.Resizes = append(c.Resizes, Resize{Disk: d.Slot, Size: fmt.Sprintf("%dG", d.SizeGB)})
	}
	return nil
}

func ctNICDiffers(o, n CTNIC) bool {
	switch {
	case n.Bridge != "" && n.Bridge != o.Bridge,
		n.Name != "" && n.Name != o.Name,
		n.MAC != "" && !strings.EqualFold(n.MAC, o.MAC),
		n.IP != "" && n.IP != o.IP,
		n.Gateway != "" && n.Gateway != o.Gateway,
		n.Firewall != nil && (o.Firewall == nil || *n.Firewall != *o.Firewall),
		(n.VLAN == nil) != (o.VLAN == nil),
		n.VLAN != nil && o.VLAN != nil && *n.VLAN != *o.VLAN:
		return true
	}
	return false
}

func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

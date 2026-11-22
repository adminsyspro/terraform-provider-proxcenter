package vmconfig

import (
	"fmt"
	"slices"
	"strings"
)

// ConfigChanges is what turns old into desired. currentRaw is the config as
// read, so a changed NIC keeps the sub-keys the provider does not manage.
// A smaller disk, or a disk or NIC no longer described, is an error: Proxmox
// cannot shrink, and the provider never removes a disk or a card on its own.
func ConfigChanges(old, desired VM, currentRaw map[string]any) (Changes, error) {
	c := Changes{Keys: map[string]any{}}
	if desired.Name != "" && desired.Name != old.Name {
		c.Keys["name"] = desired.Name
	}
	for key, pair := range map[string][2]int64{"cores": {old.Cores, desired.Cores}, "sockets": {old.Sockets, desired.Sockets}, "memory": {old.MemoryMB, desired.MemoryMB}} {
		if pair[1] != 0 && pair[0] != pair[1] {
			c.Keys[key] = pair[1]
		}
	}
	if desired.CPU != "" && desired.CPU != old.CPU {
		c.Keys["cpu"] = desired.CPU
	}
	if err := diskChanges(old.Disks, desired.Disks, &c); err != nil {
		return Changes{}, err
	}
	if err := nicChanges(old.NICs, desired.NICs, currentRaw, &c); err != nil {
		return Changes{}, err
	}
	cloudInitChanges(old.CloudInit, desired.CloudInit, &c)
	return c, nil
}

func diskChanges(old, desired []Disk, c *Changes) error {
	for _, o := range old {
		if !slices.ContainsFunc(desired, func(d Disk) bool { return d.Slot == o.Slot }) {
			return fmt.Errorf("disk %s is no longer described: remove it in ProxCenter, the provider does not detach disks", o.Slot)
		}
	}
	for _, d := range desired {
		i := slices.IndexFunc(old, func(o Disk) bool { return o.Slot == d.Slot })
		if i < 0 {
			c.Keys[d.Slot] = RenderDisk(d)
			continue
		}
		o := old[i]
		if d.SizeGB < o.SizeGB {
			return fmt.Errorf("disk %s: size %d GB to %d GB, Proxmox cannot shrink a disk", d.Slot, o.SizeGB, d.SizeGB)
		}
		if d.Storage != "" && d.Storage != o.Storage {
			c.Moves = append(c.Moves, Move{Disk: d.Slot, Storage: d.Storage})
		}
		if d.SizeGB > o.SizeGB {
			c.Resizes = append(c.Resizes, Resize{Disk: d.Slot, Size: fmt.Sprintf("%dG", d.SizeGB)})
		}
	}
	return nil
}

func nicChanges(old, desired []NIC, currentRaw map[string]any, c *Changes) error {
	for _, o := range old {
		if !slices.ContainsFunc(desired, func(n NIC) bool { return n.Slot == o.Slot }) {
			return fmt.Errorf("network %s is no longer described: describe it, or remove it in ProxCenter; the provider does not remove network cards", o.Slot)
		}
	}
	for _, n := range desired {
		i := slices.IndexFunc(old, func(o NIC) bool { return o.Slot == n.Slot })
		if i < 0 {
			c.Keys[n.Slot] = RenderNIC(n, "")
			continue
		}
		if nicDiffers(old[i], n) {
			c.Keys[n.Slot] = RenderNIC(n, str(currentRaw[n.Slot]))
		}
	}
	return nil
}

func nicDiffers(o, n NIC) bool {
	switch {
	case n.Bridge != "" && n.Bridge != o.Bridge,
		n.Model != "" && n.Model != o.Model,
		n.MAC != "" && !strings.EqualFold(n.MAC, o.MAC),
		n.Firewall != nil && (o.Firewall == nil || *n.Firewall != *o.Firewall),
		(n.VLAN == nil) != (o.VLAN == nil),
		n.VLAN != nil && o.VLAN != nil && *n.VLAN != *o.VLAN:
		return true
	}
	return false
}

func cloudInitChanges(old, desired *CloudInit, c *Changes) {
	if desired == nil {
		return
	}
	if old == nil {
		old = &CloudInit{}
	}
	// An empty field is not described: the current value stays.
	if desired.User != "" && desired.User != old.User {
		c.Keys["ciuser"] = desired.User
	}
	if desired.SSHKeys != nil && !slices.Equal(desired.SSHKeys, old.SSHKeys) {
		c.Keys["sshkeys"] = strings.Join(desired.SSHKeys, "\n")
	}
	if desired.IP != "" && (desired.IP != old.IP || desired.Gateway != old.Gateway) {
		c.Keys["ipconfig0"] = RenderIPConfig(desired.IP, desired.Gateway)
	}
	if desired.Nameserver != "" && desired.Nameserver != old.Nameserver {
		c.Keys["nameserver"] = desired.Nameserver
	}
	if desired.SearchDomain != "" && desired.SearchDomain != old.SearchDomain {
		c.Keys["searchdomain"] = desired.SearchDomain
	}
}

// NeedsReboot tells whether changes wait for a reboot.
func NeedsReboot(pending map[string]any) bool {
	return len(pending) > 0
}

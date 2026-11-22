package vmconfig

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	diskKeyRe = regexp.MustCompile(`^(scsi|virtio|sata|ide)(\d+)$`)
	nicKeyRe  = regexp.MustCompile(`^net(\d+)$`)
	sizeRe    = regexp.MustCompile(`^([0-9.]+)([KMGT]?)$`)
)

// Parse reads the described part of a Proxmox QEMU config.
func Parse(data map[string]any) (VM, error) {
	vm := VM{Sockets: 1}
	vm.Name = str(data["name"])
	vm.CPU = str(data["cpu"])
	for key, dst := range map[string]*int64{"cores": &vm.Cores, "sockets": &vm.Sockets, "memory": &vm.MemoryMB} {
		if v, ok := data[key]; ok && v != nil {
			n, err := toInt(v)
			if err != nil {
				return VM{}, fmt.Errorf("config key %s: %w", key, err)
			}
			*dst = n
		}
	}
	for key, v := range data {
		value := str(v)
		switch {
		case diskKeyRe.MatchString(key):
			if d, ok := parseDisk(key, value); ok {
				vm.Disks = append(vm.Disks, d)
			}
		case nicKeyRe.MatchString(key):
			vm.NICs = append(vm.NICs, parseNIC(key, value))
		}
	}
	sort.Slice(vm.Disks, func(i, j int) bool { return slotLess(vm.Disks[i].Slot, vm.Disks[j].Slot) })
	sort.Slice(vm.NICs, func(i, j int) bool { return slotLess(vm.NICs[i].Slot, vm.NICs[j].Slot) })
	vm.CloudInit = parseCloudInit(data)
	return vm, nil
}

func parseDisk(key, value string) (Disk, bool) {
	parts := splitOptions(value)
	if len(parts) == 0 || strings.Contains(parts[0].raw, "cloudinit") {
		return Disk{}, false
	}
	d := Disk{Slot: key}
	if i := strings.Index(parts[0].raw, ":"); i > 0 {
		d.Storage = parts[0].raw[:i]
	}
	for _, p := range parts[1:] {
		switch p.key {
		case "media":
			if p.value == "cdrom" {
				return Disk{}, false
			}
		case "size":
			d.SizeGB = parseSizeGB(p.value)
		}
	}
	return d, true
}

func parseNIC(key, value string) NIC {
	n := NIC{Slot: key}
	for i, p := range splitOptions(value) {
		if i == 0 {
			n.Model, n.MAC = p.key, p.value
			if p.value == "" && p.key == "" {
				n.Model = p.raw
			}
			continue
		}
		switch p.key {
		case "bridge":
			n.Bridge = p.value
		case "tag":
			if v, err := strconv.ParseInt(p.value, 10, 64); err == nil {
				n.VLAN = &v
			}
		case "firewall":
			b := p.value == "1"
			n.Firewall = &b
		}
	}
	if n.Firewall == nil {
		b := false
		n.Firewall = &b
	}
	return n
}

func parseCloudInit(data map[string]any) *CloudInit {
	ci := &CloudInit{
		User:         str(data["ciuser"]),
		Nameserver:   str(data["nameserver"]),
		SearchDomain: str(data["searchdomain"]),
	}
	present := ci.User != "" || ci.Nameserver != "" || ci.SearchDomain != ""
	if raw := str(data["sshkeys"]); raw != "" {
		present = true
		decoded, err := url.PathUnescape(raw)
		if err != nil {
			decoded = raw
		}
		for _, line := range strings.Split(decoded, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				ci.SSHKeys = append(ci.SSHKeys, line)
			}
		}
	}
	if ipcfg := str(data["ipconfig0"]); ipcfg != "" {
		present = true
		for _, p := range splitOptions(ipcfg) {
			switch p.key {
			case "ip":
				ci.IP = p.value
			case "gw":
				ci.Gateway = p.value
			}
		}
	}
	if !present {
		return nil
	}
	return ci
}

// parseSizeGB turns a Proxmox size (64G, 512M, 1T, bytes) into whole GB,
// rounded up so a disk never looks smaller than it is.
func parseSizeGB(s string) int64 {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	perGB := map[string]float64{"": 1 << 30, "K": 1 << 20, "M": 1 << 10, "G": 1, "T": 1.0 / 1024}[m[2]]
	return int64(math.Ceil(n/perGB - 1e-9))
}

type option struct{ raw, key, value string }

// splitOptions splits "a=b,c,d=e" keeping each item's raw text.
func splitOptions(s string) []option {
	var out []option
	for _, item := range strings.Split(s, ",") {
		if item == "" {
			continue
		}
		o := option{raw: item}
		if i := strings.Index(item, "="); i >= 0 {
			o.key, o.value = item[:i], item[i+1:]
		}
		out = append(out, o)
	}
	return out
}

func slotLess(a, b string) bool {
	pa, na := splitSlot(a)
	pb, nb := splitSlot(b)
	if pa != pb {
		return pa < pb
	}
	return na < nb
}

func splitSlot(s string) (string, int) {
	i := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return s, 0
	}
	n, _ := strconv.Atoi(s[i:])
	return s[:i], n
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func toInt(v any) (int64, error) {
	switch t := v.(type) {
	case float64:
		return int64(t), nil
	case int:
		return int64(t), nil
	case int64:
		return t, nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(t), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected value %v", v)
	}
}

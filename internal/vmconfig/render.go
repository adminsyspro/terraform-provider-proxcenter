package vmconfig

import (
	"fmt"
	"strconv"
	"strings"
)

// RenderDisk is the config value that creates a new disk.
func RenderDisk(d Disk) string {
	return fmt.Sprintf("%s:%d", d.Storage, d.SizeGB)
}

// RenderNIC is the netN value for n. Starting from current (empty for a new
// card), it changes only what n describes and keeps every other sub-key.
func RenderNIC(n NIC, current string) string {
	opts := splitOptions(current)
	model, mac := "", ""
	var rest []option
	if len(opts) > 0 {
		model, mac = opts[0].key, opts[0].value
		if model == "" {
			model = opts[0].raw
		}
		rest = opts[1:]
	}
	if n.Model != "" {
		model = n.Model
	}
	if n.MAC != "" {
		mac = n.MAC
	}
	if model == "" {
		model = "virtio"
	}
	head := model
	if mac != "" {
		head += "=" + mac
	}

	set := map[string]string{"bridge": n.Bridge}
	if n.VLAN != nil {
		set["tag"] = strconv.FormatInt(*n.VLAN, 10)
	}
	if n.Firewall != nil {
		set["firewall"] = boolFlag(*n.Firewall)
	}
	parts := []string{head}
	seen := map[string]bool{}
	for _, o := range rest {
		switch {
		case o.key == "tag" && n.VLAN == nil:
			continue // the VLAN is no longer described: untagged
		case set[o.key] != "":
			parts = append(parts, o.key+"="+set[o.key])
			seen[o.key] = true
		default:
			parts = append(parts, o.raw)
		}
	}
	for _, k := range []string{"bridge", "tag", "firewall"} {
		if v := set[k]; v != "" && !seen[k] {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

// RenderIPConfig is the ipconfig0 value; empty when ip is empty.
func RenderIPConfig(ip, gw string) string {
	if ip == "" {
		return ""
	}
	out := "ip=" + ip
	if gw != "" {
		out += ",gw=" + gw
	}
	return out
}

func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

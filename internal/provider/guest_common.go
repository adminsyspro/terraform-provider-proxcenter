package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adminsyspro/terraform-provider-proxcenter/internal/client"
	"github.com/adminsyspro/terraform-provider-proxcenter/internal/vmconfig"
)

// guestOps are the API steps a VM and a container share, for one guest type.
type guestOps struct {
	c   *client.Client
	typ client.GuestType
}

func (r *vmResource) ops() guestOps { return guestOps{c: r.client, typ: client.GuestQEMU} }

// findVM finds a guest in the connection's list, waiting for the list to
// catch up; nil when it is still absent after vmListLag.
func (g guestOps) findVM(ctx context.Context, conn string, vmid int) (*client.VMSummary, error) {
	deadline := time.Now().Add(vmListLag)
	wait := pollFirst
	for {
		vms, err := g.c.ListVMs(ctx, conn, []int{vmid})
		if err != nil {
			return nil, err
		}
		for i := range vms {
			if vms[i].VMID == vmid && (vms[i].Type == "" || vms[i].Type == string(g.typ)) {
				return &vms[i], nil
			}
		}
		if time.Now().Add(wait).After(deadline) {
			return nil, nil
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return nil, err
		}
		wait = nextWait(wait)
	}
}

// ownVM tells whether vmid is the VM this resource asked for: it exists and
// bears the requested name. It runs even after the create context expired,
// so a timeout never loses a VM the provider made.
func (g guestOps) ownVM(ctx context.Context, conn, node string, vmid int, name string) bool {
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	cfg, err := g.c.GetConfig(probe, conn, node, g.typ, vmid)
	return err == nil && (cfg.Data["name"] == name || cfg.Data["hostname"] == name)
}

// waitProvisioning waits until a clone has set up its IPAM addresses, QoS
// and HA, which ProxCenter does after the clone task ended.
func (g guestOps) waitProvisioning(ctx context.Context, conn, node string, vmid int) error {
	wait := pollFirst
	for {
		cfg, err := g.c.GetConfig(ctx, conn, node, g.typ, vmid)
		if err != nil {
			return fmt.Errorf("reading the clone: %w", err)
		}
		p := cfg.Provisioning
		if p == nil {
			return nil
		}
		if p.State == "failed" {
			return fmt.Errorf("ProxCenter could not finish setting up clone %d: %s", vmid, strings.Join(p.Failures, "; "))
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return fmt.Errorf("clone %d still provisioning: %w", vmid, err)
		}
		wait = nextWait(wait)
	}
}

// apply writes the config keys in one call, then moves and grows disks.
func (g guestOps) apply(ctx context.Context, conn, node string, vmid int, c vmconfig.Changes) error {
	if len(c.Keys) > 0 {
		if err := g.c.UpdateConfig(ctx, conn, node, g.typ, vmid, c.Keys); err != nil {
			return err
		}
	}
	for _, m := range c.Moves {
		if err := g.c.MoveDisk(ctx, conn, node, g.typ, vmid, m.Disk, m.Storage); err != nil {
			return fmt.Errorf("moving disk %s to %s: %w", m.Disk, m.Storage, err)
		}
	}
	for _, rs := range c.Resizes {
		if err := g.c.ResizeDisk(ctx, conn, node, g.typ, vmid, rs.Disk, rs.Size); err != nil {
			return fmt.Errorf("growing disk %s to %s: %w", rs.Disk, rs.Size, err)
		}
	}
	return nil
}

// powerStatus is the live status of a guest, read from Proxmox through the
// status route; a ProxCenter without that route falls back to the cached
// list, which lags a power change by up to 30 s. "" when neither knows.
func (g guestOps) powerStatus(ctx context.Context, conn, node string, vmid int) string {
	if status, err := g.c.GuestStatus(ctx, conn, node, g.typ, vmid); err == nil && status != "" && status != "unknown" {
		return status
	}
	if sum, err := g.c.CachedVM(ctx, conn, vmid); err == nil && sum != nil {
		return sum.Status
	}
	return ""
}

// reboot restarts the VM by ACPI; a guest that ignores it (one still
// booting, one without ACPI support) is hard stopped and started again only
// when force_stop allows it.
func (g guestOps) reboot(ctx context.Context, conn, node string, vmid int, force bool) error {
	err := g.c.Power(ctx, conn, node, g.typ, vmid, "reboot")
	if err == nil {
		return nil
	}
	if !force {
		return fmt.Errorf("the VM did not reboot by ACPI (%w); set force_stop = true to let the provider hard stop and start it", err)
	}
	if err := g.c.Power(ctx, conn, node, g.typ, vmid, "stop"); err != nil {
		return err
	}
	return g.c.Power(ctx, conn, node, g.typ, vmid, "start")
}

// stop shuts the VM down by ACPI, then hard stops it only when allowed.
func (g guestOps) stop(ctx context.Context, conn, node string, vmid int, force bool) error {
	err := g.c.Power(ctx, conn, node, g.typ, vmid, "shutdown")
	if err == nil || client.IsNotFound(err) {
		return nil
	}
	if !force {
		return fmt.Errorf("the VM did not stop after an ACPI shutdown (%w); set force_stop = true to hard stop it", err)
	}
	return g.c.Power(ctx, conn, node, g.typ, vmid, "stop")
}

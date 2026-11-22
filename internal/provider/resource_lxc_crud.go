package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/adminsyspro/terraform-provider-proxcenter/internal/client"
	"github.com/adminsyspro/terraform-provider-proxcenter/internal/vmconfig"
)

func (r *lxcResource) ops() guestOps { return guestOps{c: r.client, typ: client.GuestLXC} }

func (r *lxcResource) pickVMID(ctx context.Context, plan lxcModel) (int, bool, error) {
	if !plan.VMID.IsNull() && !plan.VMID.IsUnknown() {
		return int(plan.VMID.ValueInt64()), false, nil
	}
	id, err := r.client.NextID(ctx, plan.ConnectionID.ValueString())
	return id, true, err
}

func (r *lxcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lxcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var password, targetNode types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("password_wo"), &password)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("target_node"), &targetNode)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Create(ctx, 20*time.Minute)
	resp.Diagnostics.Append(diags...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn := plan.ConnectionID.ValueString()
	var (
		vmid   int
		node   string
		exists bool
		err    error
	)
	if plan.Source.CloneOf.IsNull() {
		vmid, node, exists, err = r.createFromTemplate(ctx, plan, targetNode.ValueString(), password.ValueString())
	} else {
		vmid, node, exists, err = r.createFromClone(ctx, plan, targetNode.ValueString())
	}
	if exists {
		partial := plan
		partial.ID, partial.VMID, partial.Node = types.StringValue(vmID(conn, int64(vmid))), types.Int64Value(int64(vmid)), types.StringValue(node)
		if partial.TargetNode.IsUnknown() {
			partial.TargetNode = types.StringValue(node)
		}
		nullUnknownsCT(&partial)
		resp.Diagnostics.Append(resp.State.Set(ctx, &partial)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Creating the container failed", err.Error())
		return
	}
	if err := r.configureCreated(ctx, conn, node, vmid, plan); err != nil {
		resp.Diagnostics.AddError("Configuring the new container failed", err.Error())
		return
	}
	if plan.PowerState.ValueString() == "running" {
		if err := r.client.Power(ctx, conn, node, client.GuestLXC, vmid, "start"); err != nil {
			resp.Diagnostics.AddError("Starting the new container failed", err.Error())
			return
		}
	}
	final, found, err := r.readInto(ctx, conn, node, vmid, plan, false)
	if err != nil || !found {
		resp.Diagnostics.AddError("Reading the new container failed", errText(err, "the container is not found after its creation"))
		return
	}
	final.PowerState = plan.PowerState
	if final.TargetNode.IsUnknown() {
		final.TargetNode = types.StringValue(node)
	}
	if !plan.Unprivileged.IsUnknown() && !plan.Unprivileged.Equal(final.Unprivileged) {
		// Terraform takes no other value than the configured one here; the
		// next refresh reads the real one, kept by the creation-only rule.
		resp.Diagnostics.AddWarning("Unprivileged container",
			"ProxCenter created the container unprivileged, as it does for every vDC tenant container; the next refresh records it.")
		final.Unprivileged = plan.Unprivileged
	}
	nullUnknownsCT(&final)
	resp.Diagnostics.Append(resp.State.Set(ctx, &final)...)
}

// createBody is the Proxmox create body of a container from its plan.
func createBody(plan lxcModel, vmid int, password string) map[string]any {
	ct := modelToCT(plan)
	body := map[string]any{
		"vmid": vmid, "hostname": ct.Hostname, "ostemplate": plan.Source.OSTemplate.ValueString(),
		"rootfs": vmconfig.RenderMountPoint(vmconfig.MountPoint{Storage: ct.RootFS.Storage, SizeGB: ct.RootFS.SizeGB}),
		"cores":  ct.Cores, "memory": ct.MemoryMB, "swap": ct.SwapMB, "start": 0,
	}
	if plan.Unprivileged.ValueBool() {
		body["unprivileged"] = 1
	} else {
		body["unprivileged"] = 0
	}
	for _, mp := range ct.MountPoints {
		body[mp.Slot] = vmconfig.RenderMountPoint(mp)
	}
	for _, n := range ct.NICs {
		body[n.Slot] = vmconfig.RenderCTNIC(n, "")
	}
	if ct.Features != nil {
		body["features"] = vmconfig.RenderFeatures(ct.Features)
	}
	if keys := normalizeKeys(plan.SSHKeys); len(keys) > 0 && !plan.SSHKeys.IsUnknown() {
		body["ssh-public-keys"] = strings.Join(keys, "\n")
	}
	if password != "" {
		body["password"] = password
	}
	if v := plan.Nameserver.ValueString(); v != "" && !plan.Nameserver.IsUnknown() {
		body["nameserver"] = v
	}
	if v := plan.SearchDomain.ValueString(); v != "" && !plan.SearchDomain.IsUnknown() {
		body["searchdomain"] = v
	}
	return body
}

func (r *lxcResource) createFromTemplate(ctx context.Context, plan lxcModel, node, password string) (int, string, bool, error) {
	conn := plan.ConnectionID.ValueString()
	for attempt := 1; ; attempt++ {
		vmid, auto, err := r.pickVMID(ctx, plan)
		if err != nil {
			return 0, "", false, fmt.Errorf("getting a VMID: %w", err)
		}
		upid, taskNode, err := r.client.StartCreateGuest(ctx, conn, node, client.GuestLXC, createBody(plan, vmid, password))
		if err != nil {
			if auto && client.IsVMIDClash(err) {
				if attempt < maxVMIDAttempts {
					continue
				}
				return 0, "", false, fmt.Errorf("%w\n(no free VMID after %d attempts)", err, maxVMIDAttempts)
			}
			return 0, "", false, err // refused: nothing was created
		}
		if upid != "" {
			if err := r.client.WaitTask(ctx, conn, taskNode, upid); err != nil {
				// Accepted, then failed or still running: ours only if it exists
				// under the requested hostname.
				own := r.ops().ownVM(ctx, conn, node, vmid, plan.Hostname.ValueString())
				return vmid, node, own, fmt.Errorf("creating container %d: %w", vmid, err)
			}
		}
		return vmid, node, true, nil
	}
}

func (r *lxcResource) createFromClone(ctx context.Context, plan lxcModel, configTarget string) (int, string, bool, error) {
	conn := plan.ConnectionID.ValueString()
	srcID := int(plan.Source.CloneOf.ValueInt64())
	src, err := r.ops().findVM(ctx, conn, srcID)
	if err != nil {
		return 0, "", false, fmt.Errorf("finding container %d to clone: %w", srcID, err)
	}
	if src == nil {
		return 0, "", false, fmt.Errorf("container %d is not found on connection %s, or this token cannot see it", srcID, conn)
	}
	target := configTarget
	if target == "" {
		target = src.Node
	}
	req := client.CloneRequest{Hostname: plan.Hostname.ValueString(), Full: true}
	if target != src.Node {
		req.Target = target
	}
	for attempt := 1; ; attempt++ {
		vmid, auto, err := r.pickVMID(ctx, plan)
		if err != nil {
			return 0, "", false, fmt.Errorf("getting a VMID: %w", err)
		}
		req.NewID = vmid
		upid, taskNode, err := r.client.StartClone(ctx, conn, src.Node, client.GuestLXC, srcID, req)
		if err != nil {
			if auto && client.IsVMIDClash(err) {
				if attempt < maxVMIDAttempts {
					continue
				}
				return 0, "", false, fmt.Errorf("%w\n(no free VMID after %d attempts)", err, maxVMIDAttempts)
			}
			return 0, "", false, err
		}
		if upid != "" {
			if err := r.client.WaitTask(ctx, conn, taskNode, upid); err != nil {
				return vmid, target, true, fmt.Errorf("cloning container %d into %d: %w", srcID, vmid, err)
			}
		}
		return vmid, target, true, r.ops().waitProvisioning(ctx, conn, target, vmid)
	}
}

// configureCreated brings a new container to the described config (a clone
// keeps its template's settings until changed here).
func (r *lxcResource) configureCreated(ctx context.Context, conn, node string, vmid int, plan lxcModel) error {
	cfg, err := r.client.GetConfig(ctx, conn, node, client.GuestLXC, vmid)
	if err != nil {
		return err
	}
	current, err := vmconfig.ParseCT(cfg.Data)
	if err != nil {
		return err
	}
	desired := modelToCT(plan)
	current.MountPoints = onlySlots(current.MountPoints, desired.MountPoints, func(m vmconfig.MountPoint) string { return m.Slot })
	current.NICs = onlySlots(current.NICs, desired.NICs, func(n vmconfig.CTNIC) string { return n.Slot })
	changes, err := vmconfig.CTChanges(current, desired, cfg.Data)
	if err != nil {
		return err
	}
	return r.ops().apply(ctx, conn, node, vmid, changes)
}

func (r *lxcResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lxcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	importing := state.Hostname.IsNull()
	m, found, err := r.readInto(ctx, state.ConnectionID.ValueString(), state.Node.ValueString(), int(state.VMID.ValueInt64()), state, importing)
	if err != nil {
		resp.Diagnostics.AddError("Reading the container failed", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// readInto refreshes prior from the API, as the VM resource does: mount
// points and cards by slot, everything on import, 404 only means gone.
func (r *lxcResource) readInto(ctx context.Context, conn, node string, vmid int, prior lxcModel, importing bool) (lxcModel, bool, error) {
	cfg, err := r.client.GetConfig(ctx, conn, node, client.GuestLXC, vmid)
	if client.IsNotFound(err) {
		return prior, false, nil
	}
	if err != nil {
		return prior, false, err
	}
	if cfg.MovedTo != "" {
		node = cfg.MovedTo
	}
	merged := make(map[string]any, len(cfg.Data)+len(cfg.Pending))
	for k, v := range cfg.Data {
		merged[k] = v
	}
	for k, v := range cfg.Pending {
		if k != "delete" {
			merged[k] = v
		}
	}
	ct, err := vmconfig.ParseCT(merged)
	if err != nil {
		return prior, false, err
	}
	m := prior
	m.ID, m.ConnectionID, m.VMID, m.Node = types.StringValue(vmID(conn, int64(vmid))), types.StringValue(conn), types.Int64Value(int64(vmid)), types.StringValue(node)
	m.Hostname, m.Cores, m.MemoryMB, m.SwapMB = types.StringValue(ct.Hostname), types.Int64Value(ct.Cores), types.Int64Value(ct.MemoryMB), types.Int64Value(ct.SwapMB)
	m.Unprivileged = types.BoolValue(ct.Unprivileged)
	m.Features = types.MapNull(types.BoolType)
	if ct.Features != nil {
		vals := map[string]bool{}
		for k, v := range ct.Features {
			vals[k] = v
		}
		m.Features, _ = types.MapValueFrom(ctx, types.BoolType, vals)
	}
	m.RootFS = &rootfsModel{Storage: types.StringValue(ct.RootFS.Storage), SizeGB: types.Int64Value(ct.RootFS.SizeGB)}
	m.MountPoints = readMountPoints(ct.MountPoints, prior.MountPoints, importing)
	m.Networks = readCTNICs(ct.NICs, prior.Networks, importing)
	m.Nameserver, m.SearchDomain = optionalString(ct.Nameserver), optionalString(ct.SearchDomain)
	if importing || m.SSHKeys.IsUnknown() {
		m.SSHKeys = types.ListNull(types.StringType)
	}
	m.PasswordWO = types.StringNull()
	switch status := r.ops().powerStatus(ctx, conn, node, vmid); {
	case status == "running":
		m.PowerState = types.StringValue("running")
	case status != "":
		m.PowerState = types.StringValue("stopped")
	case m.PowerState.IsNull() || m.PowerState.IsUnknown():
		m.PowerState = types.StringValue("running")
	}
	return m, true, nil
}

func readMountPoints(have []vmconfig.MountPoint, prior []mountPointModel, importing bool) []mountPointModel {
	toModel := func(mp vmconfig.MountPoint) mountPointModel {
		return mountPointModel{types.StringValue(mp.Slot), types.StringValue(mp.Storage), types.Int64Value(mp.SizeGB), types.StringValue(mp.Path)}
	}
	var out []mountPointModel
	if importing {
		for _, mp := range have {
			out = append(out, toModel(mp))
		}
		return out
	}
	for _, p := range prior {
		for _, mp := range have {
			if mp.Slot == p.Slot.ValueString() {
				out = append(out, toModel(mp))
			}
		}
	}
	return out
}

func readCTNICs(have []vmconfig.CTNIC, prior []ctNetworkModel, importing bool) []ctNetworkModel {
	priorBySlot := map[string]ctNetworkModel{}
	for i, p := range prior {
		slot := p.Slot.ValueString()
		if p.Slot.IsUnknown() || slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		priorBySlot[slot] = p
	}
	toModel := func(n vmconfig.CTNIC) ctNetworkModel {
		m := ctNetworkModel{Slot: types.StringValue(n.Slot), Name: types.StringValue(n.Name), Bridge: types.StringValue(n.Bridge),
			VLAN: types.Int64Null(), IP: optionalString(n.IP), Gateway: optionalString(n.Gateway),
			Firewall: types.BoolValue(n.Firewall != nil && *n.Firewall), MACAddress: optionalString(n.MAC)}
		if n.VLAN != nil {
			m.VLAN = types.Int64Value(*n.VLAN)
		}
		if p, ok := priorBySlot[n.Slot]; ok && !p.MACAddress.IsUnknown() && strings.EqualFold(p.MACAddress.ValueString(), n.MAC) {
			m.MACAddress = p.MACAddress
		}
		return m
	}
	var out []ctNetworkModel
	if importing {
		for _, n := range have {
			out = append(out, toModel(n))
		}
		return out
	}
	for i, p := range prior {
		slot := p.Slot.ValueString()
		if p.Slot.IsUnknown() || slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		for _, n := range have {
			if n.Slot == slot {
				out = append(out, toModel(n))
			}
		}
	}
	return out
}

// nullUnknownsCT turns the computed values still unknown into nulls.
func nullUnknownsCT(m *lxcModel) {
	for _, v := range []*types.String{&m.ID, &m.Node, &m.TargetNode, &m.Nameserver, &m.SearchDomain, &m.PowerState} {
		if v.IsUnknown() {
			*v = types.StringNull()
		}
	}
	if m.VMID.IsUnknown() {
		m.VMID = types.Int64Null()
	}
	if m.SwapMB.IsUnknown() {
		m.SwapMB = types.Int64Null()
	}
	if m.Unprivileged.IsUnknown() {
		m.Unprivileged = types.BoolNull()
	}
	if m.Features.IsUnknown() {
		m.Features = types.MapNull(types.BoolType)
	}
	if m.SSHKeys.IsUnknown() {
		m.SSHKeys = types.ListNull(types.StringType)
	}
	m.PasswordWO = types.StringNull()
	for i := range m.Networks {
		n := &m.Networks[i]
		for _, v := range []*types.String{&n.Slot, &n.Name, &n.IP, &n.Gateway, &n.MACAddress} {
			if v.IsUnknown() {
				*v = types.StringNull()
			}
		}
		if n.Firewall.IsUnknown() {
			n.Firewall = types.BoolNull()
		}
	}
}

func (r *lxcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lxcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Delete(ctx, 10*time.Minute)
	resp.Diagnostics.Append(diags...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, node, vmid := state.ConnectionID.ValueString(), state.Node.ValueString(), int(state.VMID.ValueInt64())
	force, ops := state.ForceStop.ValueBool(), r.ops()
	if ops.powerStatus(ctx, conn, node, vmid) == "running" {
		if err := ops.stop(ctx, conn, node, vmid, force); err != nil {
			resp.Diagnostics.AddError("Stopping the container failed", err.Error())
			return
		}
	}
	err := r.client.DeleteVM(ctx, conn, node, client.GuestLXC, vmid)
	if err != nil && mustStopFirst(err) {
		if err := ops.stop(ctx, conn, node, vmid, force); err != nil {
			resp.Diagnostics.AddError("Stopping the container failed", err.Error())
			return
		}
		err = r.client.DeleteVM(ctx, conn, node, client.GuestLXC, vmid)
	}
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Deleting the container failed", err.Error())
	}
}

func (r *lxcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	conn, vmid, err := parseVMID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	ct, err := r.ops().findVM(ctx, conn, vmid)
	if err != nil {
		resp.Diagnostics.AddError("Finding the container failed", err.Error())
		return
	}
	if ct == nil {
		resp.Diagnostics.AddError("Container not found", fmt.Sprintf("Container %d is not found on connection %s, or this token cannot see it.", vmid, conn))
		return
	}
	for attr, v := range map[string]any{
		"id": vmID(conn, int64(vmid)), "connection_id": conn, "vmid": int64(vmid), "node": ct.Node,
		"reboot_on_change": false, "force_stop": false,
	} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attr), v)...)
	}
}

func (r *lxcResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Not implemented", "update")
}

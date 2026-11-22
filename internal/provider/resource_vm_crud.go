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

// Waits between two reads of a deployment or of a clone still provisioning;
// tests shorten them.
var (
	pollFirst = 2 * time.Second
	pollMax   = 10 * time.Second
)

const maxVMIDAttempts = 5

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func nextWait(d time.Duration) time.Duration { return min(time.Duration(float64(d)*1.5), pollMax) }

func (r *vmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var ciConfig *cloudInitModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cloud_init"), &ciConfig)...)
	var targetNode types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("target_node"), &targetNode)...)
	if resp.Diagnostics.HasError() {
		return
	}
	password := ""
	if ciConfig != nil {
		password = ciConfig.PasswordWO.ValueString()
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
		vmid, node, exists, err = r.createFromCatalogue(ctx, plan, targetNode.ValueString(), password)
	} else {
		vmid, node, exists, err = r.createFromClone(ctx, plan, targetNode.ValueString())
	}
	if exists {
		// From here the VM is ours: keep it in state so a failure leaves it
		// tainted and `terraform destroy` removes it.
		partial := plan
		partial.ID, partial.VMID, partial.Node = types.StringValue(vmID(conn, int64(vmid))), types.Int64Value(int64(vmid)), types.StringValue(node)
		if partial.TargetNode.IsUnknown() {
			partial.TargetNode = types.StringValue(node)
		}
		nullUnknowns(&partial)
		resp.Diagnostics.Append(resp.State.Set(ctx, &partial)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Creating the VM failed", err.Error())
		return
	}

	if err := r.configureCreated(ctx, conn, node, vmid, plan, password); err != nil {
		resp.Diagnostics.AddError("Configuring the new VM failed", err.Error())
		return
	}
	if plan.PowerState.ValueString() == "running" {
		if err := r.client.Power(ctx, conn, node, vmid, "start"); err != nil {
			resp.Diagnostics.AddError("Starting the new VM failed", err.Error())
			return
		}
	}
	final, found, err := r.readInto(ctx, conn, node, vmid, plan, false)
	if err != nil || !found {
		resp.Diagnostics.AddError("Reading the new VM failed", errText(err, "the VM is not found after its creation"))
		return
	}
	// The list ProxCenter reports power from lags a start by a few seconds.
	final.PowerState = plan.PowerState
	if final.TargetNode.IsUnknown() {
		final.TargetNode = types.StringValue(node)
	}
	nullUnknowns(&final)
	resp.Diagnostics.Append(resp.State.Set(ctx, &final)...)
}

func errText(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

// pickVMID is the configured VMID, else the next free one.
func (r *vmResource) pickVMID(ctx context.Context, plan vmModel) (int, bool, error) {
	if !plan.VMID.IsNull() && !plan.VMID.IsUnknown() {
		return int(plan.VMID.ValueInt64()), false, nil
	}
	id, err := r.client.NextID(ctx, plan.ConnectionID.ValueString())
	return id, true, err
}

func (r *vmResource) createFromCatalogue(ctx context.Context, plan vmModel, node, password string) (int, string, bool, error) {
	conn := plan.ConnectionID.ValueString()
	image := plan.Source.Image.ValueString()
	if !plan.Source.Blueprint.IsNull() {
		bp, err := r.client.Blueprint(ctx, plan.Source.Blueprint.ValueString())
		if err != nil {
			return 0, "", false, fmt.Errorf("reading blueprint %s: %w", plan.Source.Blueprint.ValueString(), err)
		}
		image = bp.ImageSlug
	}
	d0, n0 := plan.Disks[0], plan.Networks[0]
	hw := client.DeployHardware{
		Cores: plan.Cores.ValueInt64(), Sockets: plan.Sockets.ValueInt64(), Memory: plan.MemoryMB.ValueInt64(),
		DiskSize: fmt.Sprintf("%dG", d0.SizeGB.ValueInt64()), DiskBus: strings.TrimRight(d0.Slot.ValueString(), "0123456789"),
		NetworkBridge: n0.Bridge.ValueString(), NetworkModel: n0.Model.ValueString(),
	}
	if !n0.VLAN.IsNull() {
		v := n0.VLAN.ValueInt64()
		hw.VLANTag = &v
	}
	if !plan.CPU.IsUnknown() && !plan.CPU.IsNull() {
		hw.CPU = plan.CPU.ValueString()
	}
	deploy := client.DeployRequest{
		ConnectionID: conn, Node: node, Storage: d0.Storage.ValueString(), VMName: plan.Name.ValueString(),
		ImageSlug: image, BlueprintID: plan.Source.Blueprint.ValueString(), Start: false, Hardware: hw,
		CloudInit: deployCloudInit(plan.CloudInit, password),
	}
	for attempt := 1; ; attempt++ {
		vmid, auto, err := r.pickVMID(ctx, plan)
		if err != nil {
			return 0, "", false, fmt.Errorf("getting a VMID: %w", err)
		}
		deploy.VMID = vmid
		res, err := r.client.Deploy(ctx, deploy)
		if err != nil {
			if auto && client.IsVMIDClash(err) {
				if attempt < maxVMIDAttempts {
					continue
				}
				return 0, "", false, fmt.Errorf("%w\n(no free VMID after %d attempts)", err, maxVMIDAttempts)
			}
			return 0, "", false, err
		}
		dep, err := r.waitDeployment(ctx, res.DeploymentID)
		if err != nil {
			return vmid, node, r.vmExists(ctx, conn, node, vmid), fmt.Errorf("following deployment %s: %w", res.DeploymentID, err)
		}
		if dep.Node != "" {
			node = dep.Node
		}
		if dep.Status == "failed" {
			if auto && client.IsVMIDClashText(dep.Error) && attempt < maxVMIDAttempts {
				continue // another guest holds that VMID: never ours to keep
			}
			exists := !client.IsVMIDClashText(dep.Error) && r.vmExists(ctx, conn, node, vmid)
			return vmid, node, exists, fmt.Errorf("deployment %s failed at %s: %s", res.DeploymentID, dep.CurrentStep, dep.Error)
		}
		return vmid, node, true, nil
	}
}

func deployCloudInit(ci *cloudInitModel, password string) *client.DeployCloudInit {
	if ci == nil && password == "" {
		return nil
	}
	out := &client.DeployCloudInit{CIPassword: password}
	if ci != nil {
		vm := modelToVM(vmModel{CloudInit: ci}).CloudInit
		out.CIUser, out.Nameserver, out.SearchDomain = vm.User, vm.Nameserver, vm.SearchDomain
		out.SSHKeys = strings.Join(vm.SSHKeys, "\n")
		out.IPConfig0 = vmconfig.RenderIPConfig(vm.IP, vm.Gateway)
	}
	return out
}

func (r *vmResource) waitDeployment(ctx context.Context, id string) (client.Deployment, error) {
	wait := pollFirst
	for {
		dep, err := r.client.GetDeployment(ctx, id)
		if err != nil {
			return dep, err
		}
		if dep.Status == "completed" || dep.Status == "failed" {
			return dep, nil
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return dep, fmt.Errorf("deployment still %s: %w", dep.Status, err)
		}
		wait = nextWait(wait)
	}
}

func (r *vmResource) vmExists(ctx context.Context, conn, node string, vmid int) bool {
	_, err := r.client.GetConfig(ctx, conn, node, vmid)
	return err == nil
}

func (r *vmResource) createFromClone(ctx context.Context, plan vmModel, configTarget string) (int, string, bool, error) {
	conn := plan.ConnectionID.ValueString()
	srcID := int(plan.Source.CloneOf.ValueInt64())
	srcs, err := r.client.ListVMs(ctx, conn, []int{srcID})
	if err != nil {
		return 0, "", false, fmt.Errorf("finding VM %d to clone: %w", srcID, err)
	}
	var src *client.VMSummary
	for i := range srcs {
		if srcs[i].VMID == srcID {
			src = &srcs[i]
		}
	}
	if src == nil {
		return 0, "", false, fmt.Errorf("VM %d is not found on connection %s, or this token cannot see it", srcID, conn)
	}
	target := configTarget
	if target == "" {
		target = src.Node
	}
	req := client.CloneRequest{Name: plan.Name.ValueString(), Full: true}
	if target != src.Node {
		req.Target = target
	}
	for attempt := 1; ; attempt++ {
		vmid, auto, err := r.pickVMID(ctx, plan)
		if err != nil {
			return 0, "", false, fmt.Errorf("getting a VMID: %w", err)
		}
		req.NewID = vmid
		if err := r.client.Clone(ctx, conn, src.Node, srcID, req); err != nil {
			if auto && client.IsVMIDClash(err) {
				if attempt < maxVMIDAttempts {
					continue
				}
				return 0, "", false, fmt.Errorf("%w\n(no free VMID after %d attempts)", err, maxVMIDAttempts)
			}
			return vmid, target, !client.IsVMIDClash(err) && r.vmExists(ctx, conn, target, vmid), err
		}
		return vmid, target, true, r.waitProvisioning(ctx, conn, target, vmid)
	}
}

// waitProvisioning waits until a clone has set up its IPAM addresses, QoS
// and HA, which ProxCenter does after the clone task ended.
func (r *vmResource) waitProvisioning(ctx context.Context, conn, node string, vmid int) error {
	wait := pollFirst
	for {
		cfg, err := r.client.GetConfig(ctx, conn, node, vmid)
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

// configureCreated brings a new VM from what the deployment or the clone
// gave to the described config: extra disks and NICs, hardware, cloud-init.
func (r *vmResource) configureCreated(ctx context.Context, conn, node string, vmid int, plan vmModel, password string) error {
	cfg, err := r.client.GetConfig(ctx, conn, node, vmid)
	if err != nil {
		return err
	}
	current, err := vmconfig.Parse(cfg.Data)
	if err != nil {
		return err
	}
	desired := modelToVM(plan)
	current.Disks = onlySlots(current.Disks, desired.Disks, func(d vmconfig.Disk) string { return d.Slot })
	current.NICs = onlySlots(current.NICs, desired.NICs, func(n vmconfig.NIC) string { return n.Slot })
	changes, err := vmconfig.ConfigChanges(current, desired, cfg.Data)
	if err != nil {
		return err
	}
	if password != "" && !plan.Source.CloneOf.IsNull() {
		changes.Keys["cipassword"] = password
	}
	return r.apply(ctx, conn, node, vmid, changes)
}

// onlySlots keeps the items of have whose slot is described in want: what a
// template carries beyond the description is left alone.
func onlySlots[T any](have, want []T, slot func(T) string) []T {
	described := map[string]bool{}
	for _, w := range want {
		described[slot(w)] = true
	}
	var out []T
	for _, h := range have {
		if described[slot(h)] {
			out = append(out, h)
		}
	}
	return out
}

// apply writes the config keys in one call, then moves and grows disks.
func (r *vmResource) apply(ctx context.Context, conn, node string, vmid int, c vmconfig.Changes) error {
	if len(c.Keys) > 0 {
		if err := r.client.UpdateConfig(ctx, conn, node, vmid, c.Keys); err != nil {
			return err
		}
	}
	for _, m := range c.Moves {
		if err := r.client.MoveDisk(ctx, conn, node, vmid, m.Disk, m.Storage); err != nil {
			return fmt.Errorf("moving disk %s to %s: %w", m.Disk, m.Storage, err)
		}
	}
	for _, rs := range c.Resizes {
		if err := r.client.ResizeDisk(ctx, conn, node, vmid, rs.Disk, rs.Size); err != nil {
			return fmt.Errorf("growing disk %s to %s: %w", rs.Disk, rs.Size, err)
		}
	}
	return nil
}

func (r *vmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, found, err := r.readInto(ctx, state.ConnectionID.ValueString(), state.Node.ValueString(), int(state.VMID.ValueInt64()), state, false)
	if err != nil {
		resp.Diagnostics.AddError("Reading the VM failed", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *vmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Delete(ctx, 10*time.Minute)
	resp.Diagnostics.Append(diags...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, node, vmid := state.ConnectionID.ValueString(), state.Node.ValueString(), int(state.VMID.ValueInt64())
	force := state.ForceStop.ValueBool()

	if sum, err := r.client.CachedVM(ctx, conn, vmid); err == nil && sum != nil && sum.Status == "running" {
		if err := r.stop(ctx, conn, node, vmid, force); err != nil {
			resp.Diagnostics.AddError("Stopping the VM failed", err.Error())
			return
		}
	}
	err := r.client.DeleteVM(ctx, conn, node, vmid)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "running") {
		// The list ProxCenter reports power from may lag: stop, then retry.
		if err := r.stop(ctx, conn, node, vmid, force); err != nil {
			resp.Diagnostics.AddError("Stopping the VM failed", err.Error())
			return
		}
		err = r.client.DeleteVM(ctx, conn, node, vmid)
	}
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Deleting the VM failed", err.Error())
	}
}

// stop shuts the VM down by ACPI, then hard stops it only when allowed.
func (r *vmResource) stop(ctx context.Context, conn, node string, vmid int, force bool) error {
	err := r.client.Power(ctx, conn, node, vmid, "shutdown")
	if err == nil || client.IsNotFound(err) {
		return nil
	}
	if !force {
		return fmt.Errorf("the VM did not stop after an ACPI shutdown (%w); set force_stop = true to hard stop it", err)
	}
	return r.client.Power(ctx, conn, node, vmid, "stop")
}

// readInto refreshes prior from the API. Only what prior describes is read
// back (disks and NICs by slot), so nothing the user leaves out drifts;
// importing reads everything. found is false only on a 404 of the config.
func (r *vmResource) readInto(ctx context.Context, conn, node string, vmid int, prior vmModel, importing bool) (vmModel, bool, error) {
	cfg, err := r.client.GetConfig(ctx, conn, node, vmid)
	if client.IsNotFound(err) {
		return prior, false, nil
	}
	if err != nil {
		return prior, false, err
	}
	if cfg.MovedTo != "" {
		node = cfg.MovedTo
	}
	vm, err := vmconfig.Parse(cfg.Data)
	if err != nil {
		return prior, false, err
	}
	m := prior
	m.ID, m.ConnectionID, m.VMID, m.Node = types.StringValue(vmID(conn, int64(vmid))), types.StringValue(conn), types.Int64Value(int64(vmid)), types.StringValue(node)
	m.Name, m.Cores, m.Sockets, m.MemoryMB = types.StringValue(vm.Name), types.Int64Value(vm.Cores), types.Int64Value(vm.Sockets), types.Int64Value(vm.MemoryMB)
	m.CPU = optionalString(vm.CPU)
	m.Disks = readDisks(vm.Disks, prior.Disks, importing)
	m.Networks = readNICs(vm.NICs, prior.Networks, importing)

	sum, sumErr := r.client.CachedVM(ctx, conn, vmid)
	if sumErr == nil && sum != nil {
		if sum.Status == "running" {
			m.PowerState = types.StringValue("running")
		} else {
			m.PowerState = types.StringValue("stopped")
		}
	} else if m.PowerState.IsNull() || m.PowerState.IsUnknown() {
		m.PowerState = types.StringValue("running")
	}
	var ips []string
	if sum != nil {
		ips = sum.IPs
	}
	m.CloudInit = readCloudInit(vm.CloudInit, prior.CloudInit, importing, ips)
	return m, true, nil
}

func readDisks(have []vmconfig.Disk, prior []diskModel, importing bool) []diskModel {
	toModel := func(d vmconfig.Disk) diskModel {
		return diskModel{types.StringValue(d.Slot), types.StringValue(d.Storage), types.Int64Value(d.SizeGB)}
	}
	var out []diskModel
	if importing {
		for _, d := range have {
			out = append(out, toModel(d))
		}
		return out
	}
	for _, p := range prior {
		for _, d := range have {
			if d.Slot == p.Slot.ValueString() {
				out = append(out, toModel(d))
			}
		}
	}
	return out
}

func readNICs(have []vmconfig.NIC, prior []networkModel, importing bool) []networkModel {
	toModel := func(n vmconfig.NIC) networkModel {
		m := networkModel{Slot: types.StringValue(n.Slot), Bridge: types.StringValue(n.Bridge), Model: types.StringValue(n.Model),
			MACAddress: optionalString(n.MAC), VLAN: types.Int64Null(), Firewall: types.BoolValue(n.Firewall != nil && *n.Firewall)}
		if n.VLAN != nil {
			m.VLAN = types.Int64Value(*n.VLAN)
		}
		return m
	}
	var out []networkModel
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

func readCloudInit(have *vmconfig.CloudInit, prior *cloudInitModel, importing bool, ips []string) *cloudInitModel {
	if prior == nil && (!importing || have == nil) {
		return nil
	}
	if have == nil {
		have = &vmconfig.CloudInit{}
	}
	m := cloudInitModel{
		User: types.StringNull(), SSHKeys: types.ListNull(types.StringType), Gateway: types.StringNull(),
		Nameserver: types.StringNull(), SearchDomain: types.StringNull(), PasswordWO: types.StringNull(), PasswordWOVersion: types.Int64Null(),
	}
	if prior != nil {
		m = *prior
		m.PasswordWO = types.StringNull()
	}
	// A field is read back when the user describes it (or on import).
	described := func(v interface{ IsNull() bool }) bool { return importing || !v.IsNull() }
	if described(m.User) {
		m.User = optionalString(have.User)
	}
	if described(m.SSHKeys) {
		if have.SSHKeys == nil && importing {
			m.SSHKeys = types.ListNull(types.StringType)
		} else {
			m.SSHKeys = stringList(have.SSHKeys)
		}
	}
	if described(m.Gateway) {
		m.Gateway = optionalString(have.Gateway)
	}
	if described(m.Nameserver) {
		m.Nameserver = optionalString(have.Nameserver)
	}
	if described(m.SearchDomain) {
		m.SearchDomain = optionalString(have.SearchDomain)
	}
	m.IP = optionalString(have.IP)
	m.IPAddress = types.StringNull()
	switch {
	case have.IP != "" && have.IP != "dhcp":
		m.IPAddress = types.StringValue(strings.SplitN(have.IP, "/", 2)[0])
	case len(ips) > 0:
		m.IPAddress = types.StringValue(ips[0])
	}
	return &m
}

// nullUnknowns turns the computed values still unknown into nulls: a state
// never holds an unknown.
func nullUnknowns(m *vmModel) {
	str := func(v *types.String) {
		if v.IsUnknown() {
			*v = types.StringNull()
		}
	}
	str(&m.ID)
	str(&m.Node)
	str(&m.TargetNode)
	str(&m.CPU)
	if m.VMID.IsUnknown() {
		m.VMID = types.Int64Null()
	}
	for i := range m.Networks {
		str(&m.Networks[i].Slot)
		str(&m.Networks[i].MACAddress)
		if m.Networks[i].Firewall.IsUnknown() {
			m.Networks[i].Firewall = types.BoolNull()
		}
	}
	if ci := m.CloudInit; ci != nil {
		str(&ci.IP)
		str(&ci.IPAddress)
		ci.PasswordWO = types.StringNull()
	}
}

func (r *vmResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Not implemented", "update")
}

func (r *vmResource) ImportState(_ context.Context, _ resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.AddError("Not implemented", "import")
}

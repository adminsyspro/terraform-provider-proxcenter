package provider

import (
	"context"
	"fmt"
	"slices"
	"sort"
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

// vmListLag bounds the wait for /api/v1/vms to list a guest: ProxCenter
// serves that list from a cache refreshed every 30 s per connection, so a
// guest created a moment ago is not in it yet.
var vmListLag = 60 * time.Second

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
		if err := r.client.Power(ctx, conn, node, client.GuestQEMU, vmid, "start"); err != nil {
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
			own := r.ops().ownVM(ctx, conn, node, vmid, plan.Name.ValueString())
			return vmid, node, own, fmt.Errorf("following deployment %s of VMID %d: %w; it may still be running in ProxCenter", res.DeploymentID, vmid, err)
		}
		if dep.Node != "" {
			node = dep.Node
		}
		if dep.Status != "failed" {
			return vmid, node, true, nil
		}
		failure := fmt.Errorf("deployment %s failed at %s: %s", res.DeploymentID, dep.CurrentStep, dep.Error)
		retryable := auto && client.IsVMIDClashText(dep.Error) && attempt < maxVMIDAttempts
		if !deploymentMayHaveCreated(dep.CurrentStep) {
			if retryable {
				continue
			}
			return 0, "", false, failure // nothing was created
		}
		own := r.ops().ownVM(ctx, conn, node, vmid, plan.Name.ValueString())
		if !own && retryable && dep.CurrentStep == "creating" {
			continue // the create itself hit a VMID another guest holds
		}
		return vmid, node, own, failure
	}
}

// deploymentMayHaveCreated tells whether a deployment that stopped at step
// may have created its VM: before "creating" nothing exists.
func deploymentMayHaveCreated(step string) bool {
	switch step {
	case "", "pending", "downloading":
		return false
	}
	return true
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

func (r *vmResource) createFromClone(ctx context.Context, plan vmModel, configTarget string) (int, string, bool, error) {
	conn := plan.ConnectionID.ValueString()
	srcID := int(plan.Source.CloneOf.ValueInt64())
	src, err := r.ops().findVM(ctx, conn, srcID)
	if err != nil {
		return 0, "", false, fmt.Errorf("finding VM %d to clone: %w", srcID, err)
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
		upid, taskNode, err := r.client.StartClone(ctx, conn, src.Node, client.GuestQEMU, srcID, req)
		if err != nil {
			// Refused: Proxmox created nothing, whatever holds that VMID.
			if auto && client.IsVMIDClash(err) {
				if attempt < maxVMIDAttempts {
					continue
				}
				return 0, "", false, fmt.Errorf("%w\n(no free VMID after %d attempts)", err, maxVMIDAttempts)
			}
			return 0, "", false, err
		}
		// Accepted: the clone is ours from here, even if waiting fails.
		if upid != "" {
			if err := r.client.WaitTask(ctx, conn, taskNode, upid); err != nil {
				return vmid, target, true, fmt.Errorf("cloning VM %d into %d: %w", srcID, vmid, err)
			}
		}
		return vmid, target, true, r.ops().waitProvisioning(ctx, conn, target, vmid)
	}
}

// configureCreated brings a new VM from what the deployment or the clone
// gave to the described config: extra disks and NICs, hardware, cloud-init.
func (r *vmResource) configureCreated(ctx context.Context, conn, node string, vmid int, plan vmModel, password string) error {
	cfg, err := r.client.GetConfig(ctx, conn, node, client.GuestQEMU, vmid)
	if err != nil {
		return err
	}
	current, err := vmconfig.Parse(cfg.Data)
	if err != nil {
		return err
	}
	desired := modelToVM(plan)
	// dhcp on an IPAM VNet was turned by ProxCenter into the address it
	// allocated: the request is met, never push dhcp back.
	if desired.CloudInit != nil && desired.CloudInit.IP == "dhcp" && current.CloudInit != nil && current.CloudInit.IP != "" {
		desired.CloudInit.IP, desired.CloudInit.Gateway = current.CloudInit.IP, current.CloudInit.Gateway
	}
	current.Disks = onlySlots(current.Disks, desired.Disks, func(d vmconfig.Disk) string { return d.Slot })
	current.NICs = onlySlots(current.NICs, desired.NICs, func(n vmconfig.NIC) string { return n.Slot })
	changes, err := vmconfig.ConfigChanges(current, desired, cfg.Data)
	if err != nil {
		return err
	}
	if password != "" && !plan.Source.CloneOf.IsNull() {
		changes.Keys["cipassword"] = password
	}
	return r.ops().apply(ctx, conn, node, vmid, changes)
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

func (r *vmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An import leaves only the identity in state: read everything.
	importing := state.Name.IsNull()
	m, found, err := r.readInto(ctx, state.ConnectionID.ValueString(), state.Node.ValueString(), int(state.VMID.ValueInt64()), state, importing)
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

	if r.ops().powerStatus(ctx, conn, node, vmid) == "running" {
		if err := r.ops().stop(ctx, conn, node, vmid, force); err != nil {
			resp.Diagnostics.AddError("Stopping the VM failed", err.Error())
			return
		}
	}
	err := r.client.DeleteVM(ctx, conn, node, client.GuestQEMU, vmid)
	if err != nil && mustStopFirst(err) {
		// The list ProxCenter reports power from may lag: stop, then retry.
		if err := r.ops().stop(ctx, conn, node, vmid, force); err != nil {
			resp.Diagnostics.AddError("Stopping the VM failed", err.Error())
			return
		}
		err = r.client.DeleteVM(ctx, conn, node, client.GuestQEMU, vmid)
	}
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Deleting the VM failed", err.Error())
	}
}

// readInto refreshes prior from the API. Only what prior describes is read
// back (disks and NICs by slot), so nothing the user leaves out drifts;
// importing reads everything. found is false only on a 404 of the config.
func (r *vmResource) readInto(ctx context.Context, conn, node string, vmid int, prior vmModel, importing bool) (vmModel, bool, error) {
	cfg, err := r.client.GetConfig(ctx, conn, node, client.GuestQEMU, vmid)
	if client.IsNotFound(err) {
		return prior, false, nil
	}
	if err != nil {
		return prior, false, err
	}
	if cfg.MovedTo != "" {
		node = cfg.MovedTo
	}
	// A change waiting for a reboot is accepted: read it as the VM's value.
	merged := make(map[string]any, len(cfg.Data)+len(cfg.Pending))
	for k, v := range cfg.Data {
		merged[k] = v
	}
	for k, v := range cfg.Pending {
		if k != "delete" {
			merged[k] = v
		}
	}
	vm, err := vmconfig.Parse(merged)
	if err != nil {
		return prior, false, err
	}
	m := prior
	m.ID, m.ConnectionID, m.VMID, m.Node = types.StringValue(vmID(conn, int64(vmid))), types.StringValue(conn), types.Int64Value(int64(vmid)), types.StringValue(node)
	m.Name, m.Cores, m.Sockets, m.MemoryMB = types.StringValue(vm.Name), types.Int64Value(vm.Cores), types.Int64Value(vm.Sockets), types.Int64Value(vm.MemoryMB)
	m.CPU = optionalString(vm.CPU)
	m.Disks = readDisks(vm.Disks, prior.Disks, importing)
	m.Networks = readNICs(vm.NICs, prior.Networks, importing)

	switch status := r.ops().powerStatus(ctx, conn, node, vmid); {
	case status == "running":
		m.PowerState = types.StringValue("running")
	case status != "":
		m.PowerState = types.StringValue("stopped")
	case m.PowerState.IsNull() || m.PowerState.IsUnknown():
		m.PowerState = types.StringValue("running")
	}
	var ips []string
	if sum, err := r.client.CachedVM(ctx, conn, vmid); err == nil && sum != nil {
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
	priorBySlot := map[string]networkModel{}
	for i, p := range prior {
		slot := p.Slot.ValueString()
		if p.Slot.IsUnknown() || slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		priorBySlot[slot] = p
	}
	toModel := func(n vmconfig.NIC) networkModel {
		m := networkModel{Slot: types.StringValue(n.Slot), Bridge: types.StringValue(n.Bridge), Model: types.StringValue(n.Model),
			MACAddress: optionalString(n.MAC), VLAN: types.Int64Null(), Firewall: types.BoolValue(n.Firewall != nil && *n.Firewall)}
		if n.VLAN != nil {
			m.VLAN = types.Int64Value(*n.VLAN)
		}
		// The same MAC written in another case is the user's value.
		if p, ok := priorBySlot[n.Slot]; ok && !p.MACAddress.IsUnknown() && strings.EqualFold(p.MACAddress.ValueString(), n.MAC) {
			m.MACAddress = p.MACAddress
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
	m := cloudInitModel{PasswordWOVersion: types.Int64Null()}
	if prior != nil {
		m.PasswordWOVersion = prior.PasswordWOVersion
	}
	// Every field is optional and computed: read back as the VM has it.
	m.PasswordWO = types.StringNull()
	m.User = optionalString(have.User)
	m.SSHKeys = types.ListNull(types.StringType)
	if len(have.SSHKeys) > 0 {
		m.SSHKeys = stringList(have.SSHKeys)
	}
	// Keys written with trailing newlines, or several per item (a whole
	// authorized_keys file), are the same keys once normalised.
	if prior != nil && !prior.SSHKeys.IsNull() && !prior.SSHKeys.IsUnknown() && slices.Equal(normalizeKeys(prior.SSHKeys), have.SSHKeys) {
		m.SSHKeys = prior.SSHKeys
	}
	m.Gateway, m.Nameserver, m.SearchDomain = optionalString(have.Gateway), optionalString(have.Nameserver), optionalString(have.SearchDomain)
	m.IP = optionalString(have.IP)
	// dhcp on an IPAM VNet comes back as the address ProxCenter allocated:
	// the request is met, ip_address tells the address.
	if prior != nil && prior.IP.ValueString() == "dhcp" && have.IP != "" {
		m.IP = prior.IP
	}
	m.IPAddress = types.StringNull()
	switch {
	case have.IP != "" && have.IP != "dhcp":
		m.IPAddress = types.StringValue(strings.SplitN(have.IP, "/", 2)[0])
	case len(ips) > 0:
		m.IPAddress = types.StringValue(ips[0])
	}
	return &m
}

// normalizeKeys is the list of keys a ssh_keys value holds, one per line.
func normalizeKeys(l types.List) []string {
	var out []string
	for _, e := range l.Elements() {
		if s, ok := e.(types.String); ok {
			for _, line := range strings.Split(s.ValueString(), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					out = append(out, line)
				}
			}
		}
	}
	return out
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
		for _, v := range []*types.String{&ci.User, &ci.IP, &ci.IPAddress, &ci.Gateway, &ci.Nameserver, &ci.SearchDomain} {
			str(v)
		}
		if ci.SSHKeys.IsUnknown() {
			ci.SSHKeys = types.ListNull(types.StringType)
		}
		ci.PasswordWO = types.StringNull()
	}
}

func (r *vmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	var ciConfig *cloudInitModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cloud_init"), &ciConfig)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Update(ctx, 20*time.Minute)
	resp.Diagnostics.Append(diags...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, node, vmid := state.ConnectionID.ValueString(), state.Node.ValueString(), int(state.VMID.ValueInt64())

	cfg, err := r.client.GetConfig(ctx, conn, node, client.GuestQEMU, vmid)
	if err != nil {
		resp.Diagnostics.AddError("Reading the VM failed", err.Error())
		return
	}
	if cfg.MovedTo != "" {
		node = cfg.MovedTo
	}
	changes, err := vmconfig.ConfigChanges(modelToVM(state), modelToVM(plan), cfg.Data)
	if err != nil {
		resp.Diagnostics.AddError("Change refused", err.Error())
		return
	}
	if passwordChanged(state, plan) && ciConfig != nil && ciConfig.PasswordWO.ValueString() != "" {
		changes.Keys["cipassword"] = ciConfig.PasswordWO.ValueString()
	}
	if err := r.ops().apply(ctx, conn, node, vmid, changes); err != nil {
		resp.Diagnostics.AddError("Updating the VM failed", err.Error())
		return
	}

	wantRunning := plan.PowerState.ValueString() == "running"
	wasRunning := state.PowerState.ValueString() == "running"
	if !changes.Empty() && wasRunning && wantRunning {
		r.handlePending(ctx, conn, node, vmid, plan, resp)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	switch {
	case wantRunning && !wasRunning:
		err = r.client.Power(ctx, conn, node, client.GuestQEMU, vmid, "start")
	case !wantRunning && wasRunning:
		err = r.ops().stop(ctx, conn, node, vmid, plan.ForceStop.ValueBool())
	}
	if err != nil {
		resp.Diagnostics.AddError("Changing the power state failed", err.Error())
		return
	}

	final, found, err := r.readInto(ctx, conn, node, vmid, plan, false)
	if err != nil || !found {
		resp.Diagnostics.AddError("Reading the VM failed", errText(err, "the VM is no longer found"))
		return
	}
	final.PowerState = plan.PowerState
	final.TargetNode = state.TargetNode
	// What the plan already knew stays: a value the guest agent or DRS
	// changed meanwhile is picked up by the next refresh.
	if !plan.Node.IsUnknown() {
		final.Node = plan.Node
	}
	if plan.CloudInit != nil && final.CloudInit != nil && !plan.CloudInit.IPAddress.IsUnknown() {
		final.CloudInit.IPAddress = plan.CloudInit.IPAddress
	}
	nullUnknowns(&final)
	resp.Diagnostics.Append(resp.State.Set(ctx, &final)...)
}

func passwordChanged(state, plan vmModel) bool {
	if plan.CloudInit == nil {
		return false
	}
	old := types.Int64Null()
	if state.CloudInit != nil {
		old = state.CloudInit.PasswordWOVersion
	}
	return !plan.CloudInit.PasswordWOVersion.IsNull() && !plan.CloudInit.PasswordWOVersion.Equal(old)
}

// handlePending reboots a running VM whose change waits for a reboot when
// reboot_on_change is set, and warns otherwise.
func (r *vmResource) handlePending(ctx context.Context, conn, node string, vmid int, plan vmModel, resp *resource.UpdateResponse) {
	cfg, err := r.client.GetConfig(ctx, conn, node, client.GuestQEMU, vmid)
	if err != nil {
		resp.Diagnostics.AddError("Reading the VM failed", err.Error())
		return
	}
	if !vmconfig.NeedsReboot(cfg.Pending) {
		return
	}
	keys := make([]string, 0, len(cfg.Pending))
	for k := range cfg.Pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if plan.RebootOnChange.ValueBool() {
		if err := r.ops().reboot(ctx, conn, node, vmid, plan.ForceStop.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Rebooting the VM failed", err.Error())
		}
		return
	}
	resp.Diagnostics.AddWarning("Reboot required",
		fmt.Sprintf("Proxmox applies the change of %s (%s) at the next reboot of the VM. Set reboot_on_change = true to let the provider reboot it.",
			plan.Name.ValueString(), strings.Join(keys, ", ")))
}

// ImportState takes <connection_id>/<vmid>; the node comes from the VM list
// once, then Read fills every attribute from the config.
func (r *vmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	conn, vmid, err := parseVMID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	vm, err := r.ops().findVM(ctx, conn, vmid)
	if err != nil {
		resp.Diagnostics.AddError("Finding the VM failed", err.Error())
		return
	}
	if vm == nil {
		resp.Diagnostics.AddError("VM not found", fmt.Sprintf("VM %d is not found on connection %s, or this token cannot see it.", vmid, conn))
		return
	}
	node := vm.Node
	for attr, v := range map[string]any{
		"id": vmID(conn, int64(vmid)), "connection_id": conn, "vmid": int64(vmid), "node": node,
		"reboot_on_change": false, "force_stop": false,
	} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attr), v)...)
	}
}

// mustStopFirst tells a delete refused because the VM still runs.
func mustStopFirst(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "running") || strings.Contains(msg, "must be stopped")
}

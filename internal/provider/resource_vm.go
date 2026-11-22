package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/adminsyspro/terraform-provider-proxcenter/internal/vmconfig"
)

var (
	_ resource.Resource                   = (*vmResource)(nil)
	_ resource.ResourceWithConfigure      = (*vmResource)(nil)
	_ resource.ResourceWithImportState    = (*vmResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*vmResource)(nil)
	_ resource.ResourceWithValidateConfig = (*vmResource)(nil)
)

type sourceModel struct {
	Image     types.String `tfsdk:"image"`
	Blueprint types.String `tfsdk:"blueprint"`
	CloneOf   types.Int64  `tfsdk:"clone_of"`
}

type diskModel struct {
	Slot    types.String `tfsdk:"slot"`
	Storage types.String `tfsdk:"storage"`
	SizeGB  types.Int64  `tfsdk:"size_gb"`
}

type networkModel struct {
	Slot       types.String `tfsdk:"slot"`
	Bridge     types.String `tfsdk:"bridge"`
	VLAN       types.Int64  `tfsdk:"vlan"`
	Model      types.String `tfsdk:"model"`
	Firewall   types.Bool   `tfsdk:"firewall"`
	MACAddress types.String `tfsdk:"mac_address"`
}

type cloudInitModel struct {
	User              types.String `tfsdk:"user"`
	SSHKeys           types.List   `tfsdk:"ssh_keys"`
	IP                types.String `tfsdk:"ip"`
	Gateway           types.String `tfsdk:"gateway"`
	Nameserver        types.String `tfsdk:"nameserver"`
	SearchDomain      types.String `tfsdk:"search_domain"`
	IPAddress         types.String `tfsdk:"ip_address"`
	PasswordWO        types.String `tfsdk:"password_wo"`
	PasswordWOVersion types.Int64  `tfsdk:"password_wo_version"`
}

type vmModel struct {
	ID             types.String    `tfsdk:"id"`
	ConnectionID   types.String    `tfsdk:"connection_id"`
	VMID           types.Int64     `tfsdk:"vmid"`
	Node           types.String    `tfsdk:"node"`
	TargetNode     types.String    `tfsdk:"target_node"`
	Name           types.String    `tfsdk:"name"`
	Source         *sourceModel    `tfsdk:"source"`
	Cores          types.Int64     `tfsdk:"cores"`
	Sockets        types.Int64     `tfsdk:"sockets"`
	MemoryMB       types.Int64     `tfsdk:"memory_mb"`
	CPU            types.String    `tfsdk:"cpu"`
	Disks          []diskModel     `tfsdk:"disk"`
	Networks       []networkModel  `tfsdk:"network"`
	CloudInit      *cloudInitModel `tfsdk:"cloud_init"`
	PowerState     types.String    `tfsdk:"power_state"`
	RebootOnChange types.Bool      `tfsdk:"reboot_on_change"`
	ForceStop      types.Bool      `tfsdk:"force_stop"`
	Timeouts       timeouts.Value  `tfsdk:"timeouts"`
}

type vmResource struct {
	clientHolder
}

func newVMResource() resource.Resource { return &vmResource{} }

func (r *vmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (r *vmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if err := r.configureFrom(req.ProviderData); err != nil {
		resp.Diagnostics.AddError("Provider misconfigured", err.Error())
	}
}

func (r *vmResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A QEMU VM managed through ProxCenter, created from the catalogue (image or blueprint) or by cloning a VM. " +
			"Quotas, allowed networks and vDC rules are enforced by ProxCenter; its refusals are shown as they are.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "<connection_id>/<vmid>, also the import id.", PlanModifiers: keep},
			"connection_id": schema.StringAttribute{Required: true, Description: "Cluster connection, e.g. data.proxcenter_vdc.x.connection_id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"vmid": schema.Int64Attribute{Optional: true, Computed: true, Description: "Unset: the next free VMID (inside the tenant range for a vDC tenant).",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace(), int64planmodifier.UseStateForUnknown()}},
			"node":        schema.StringAttribute{Computed: true, Description: "Node the VM runs on, read back on every refresh (follows DRS).", PlanModifiers: keep},
			"target_node": schema.StringAttribute{Optional: true, Computed: true, Description: "Node to create the VM on; required for a catalogue source, defaults to the source's node for a clone. Ignored after creation.", PlanModifiers: keep},
			"name":        schema.StringAttribute{Required: true},
			"cores":       schema.Int64Attribute{Required: true},
			"sockets": schema.Int64Attribute{Optional: true, Computed: true, Description: "Defaults to 1 at creation; unset keeps the VM's value.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"memory_mb": schema.Int64Attribute{Required: true},
			"cpu":       schema.StringAttribute{Optional: true, Computed: true, Description: "CPU model; unset keeps what ProxCenter set (vDC CPU policy).", PlanModifiers: keep},
			"power_state": schema.StringAttribute{Optional: true, Computed: true, Description: "running or stopped; running at creation when unset, the VM's state otherwise.",
				Validators: []validator.String{stringvalidator.OneOf("running", "stopped")}, PlanModifiers: keep},
			"reboot_on_change": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Reboot the VM when a change waits for a reboot; false only warns."},
			"force_stop": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Hard stop the VM when an ACPI shutdown does not stop it."},
		},
		Blocks: map[string]schema.Block{
			"source": schema.SingleNestedBlock{
				Description: "Where the VM comes from; required to create it, optional after an import. Changing it replaces the VM.",
				Attributes: map[string]schema.Attribute{
					"image":     schema.StringAttribute{Optional: true, Description: "Catalogue image slug (data.proxcenter_images)."},
					"blueprint": schema.StringAttribute{Optional: true, Description: "Blueprint id (data.proxcenter_blueprints)."},
					"clone_of":  schema.Int64Attribute{Optional: true, Description: "VMID of the VM or template to clone."},
				},
				PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplaceIf(sourceChangeReplaces,
					"A different source replaces the VM.", "A different source replaces the VM.")},
			},
			"disk": schema.ListNestedBlock{
				Description: "Disks by slot. A disk grows in place, moves when its storage changes; shrinking or removing one is refused at plan.",
				Validators:  []validator.List{listvalidator.IsRequired(), listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"slot":    schema.StringAttribute{Required: true, Description: "scsi0, virtio1, sata0..."},
					"storage": schema.StringAttribute{Required: true},
					"size_gb": schema.Int64Attribute{Required: true},
				}},
			},
			"network": schema.ListNestedBlock{
				Validators: []validator.List{listvalidator.IsRequired(), listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"slot":        schema.StringAttribute{Optional: true, Computed: true, Description: "net0, net1...; defaults to net<index>.", PlanModifiers: keep},
					"bridge":      schema.StringAttribute{Required: true, Description: "Bridge or VNet (pve_name of data.proxcenter_vnets)."},
					"vlan":        schema.Int64Attribute{Optional: true, Description: "VLAN tag; a vDC tenant token needs vms:nic."},
					"model":       schema.StringAttribute{Optional: true, Computed: true, Description: "virtio for a new card when unset; unset keeps an existing card's model.", PlanModifiers: keep},
					"firewall":    schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolUseStateForUnknown()}},
					"mac_address": schema.StringAttribute{Optional: true, Computed: true, Description: "A vDC tenant token needs vms:nic to set it.", PlanModifiers: keep},
				}},
			},
			"cloud_init": schema.SingleNestedBlock{
				Description: "Cloud-init settings. A field left unset keeps the value the VM has.",
				Attributes: map[string]schema.Attribute{
					"user": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep},
					"ssh_keys": schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType,
						PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
					"ip":            schema.StringAttribute{Optional: true, Computed: true, Description: "dhcp or CIDR; unset lets ProxCenter decide (an IPAM VNet allocates one).", PlanModifiers: keep},
					"gateway":       schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep},
					"nameserver":    schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep},
					"search_domain": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep},
					"ip_address":    schema.StringAttribute{Computed: true, Description: "Address the VM got.", PlanModifiers: keep},
					"password_wo": schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true,
						Description: "Cloud-init password, never stored in state (Terraform 1.11+). Change password_wo_version to send a new one."},
					"password_wo_version": schema.Int64Attribute{Optional: true},
				},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// sourceChangeReplaces: a VM imported without a source keeps its identity
// when a source is added to its configuration.
func sourceChangeReplaces(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull() && !req.PlanValue.IsNull() && !req.StateValue.Equal(req.PlanValue)
}

func (r *vmResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var nets []networkModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("network"), &nets)...)
	if len(nets) > 1 {
		for _, n := range nets {
			if n.Slot.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root("network"), "Missing network slot",
					"Set slot (net0, net1...) on every network block when there are several network blocks: a list position is not a card's identity.")
				break
			}
		}
	}
	var src *sourceModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("source"), &src)...)
	if resp.Diagnostics.HasError() || src == nil {
		return
	}
	if src.Image.IsUnknown() || src.Blueprint.IsUnknown() || src.CloneOf.IsUnknown() {
		return
	}
	set := 0
	for _, isSet := range []bool{!src.Image.IsNull(), !src.Blueprint.IsNull(), !src.CloneOf.IsNull()} {
		if isSet {
			set++
		}
	}
	if set != 1 {
		resp.Diagnostics.AddAttributeError(path.Root("source"), "Invalid source", "Set exactly one of image, blueprint or clone_of.")
	}
}

func (r *vmResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed := false
	for i := range plan.Networks {
		if plan.Networks[i].Slot.IsUnknown() || plan.Networks[i].Slot.IsNull() {
			plan.Networks[i].Slot = types.StringValue("net" + strconv.Itoa(i))
			changed = true
		}
	}

	if req.State.Raw.IsNull() {
		var targetNode types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("target_node"), &targetNode)...)
		r.planCreate(&plan, targetNode, resp)
		createDefaults(&plan)
		changed = true
	} else {
		var state vmModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		// target_node only matters at creation.
		if !plan.TargetNode.Equal(state.TargetNode) {
			plan.TargetNode = state.TargetNode
			changed = true
		}
		if planKnown(plan) {
			if _, err := vmconfig.ConfigChanges(modelToVM(state), modelToVM(plan), nil); err != nil {
				resp.Diagnostics.AddError("Change refused", err.Error())
			}
		}
		if addressMayChange(state, plan) {
			plan.CloudInit.IPAddress = types.StringUnknown()
			var configIP types.String
			if plan.CloudInit != nil {
				resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cloud_init").AtName("ip"), &configIP)...)
			}
			if configIP.IsNull() {
				// ProxCenter re-allocates on an IPAM VNet: what it picks is unknown.
				plan.CloudInit.IP, plan.CloudInit.Gateway = types.StringUnknown(), types.StringUnknown()
			}
			changed = true
		}
		if unknownNewComputed(&plan, state) {
			changed = true
		}
	}
	if changed && !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	}
}

func (r *vmResource) planCreate(plan *vmModel, configTargetNode types.String, resp *resource.ModifyPlanResponse) {
	if plan.Source == nil {
		resp.Diagnostics.AddAttributeError(path.Root("source"), "Missing source",
			"source is required to create a VM: set image, blueprint or clone_of.")
		return
	}
	catalogue := !plan.Source.Image.IsNull() || !plan.Source.Blueprint.IsNull()
	if catalogue && configTargetNode.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("target_node"), "Missing target_node",
			"A VM from the catalogue needs target_node: pick one of the nodes of its vDC (data.proxcenter_vdc.<name>.nodes).")
	}
}

// createDefaults sets what an unset attribute means for a new VM; after
// creation an unset attribute keeps the VM's value.
func createDefaults(plan *vmModel) {
	if plan.Sockets.IsUnknown() {
		plan.Sockets = types.Int64Value(1)
	}
	if plan.PowerState.IsUnknown() {
		plan.PowerState = types.StringValue("running")
	}
	for i := range plan.Networks {
		if plan.Networks[i].Model.IsUnknown() || plan.Networks[i].Model.IsNull() {
			plan.Networks[i].Model = types.StringValue("virtio")
		}
	}
}

// unknownNewComputed marks unknown the computed values of what an update
// adds (a NIC, a cloud_init block): the framework plans them null, and the
// API is what sets them.
func unknownNewComputed(plan *vmModel, state vmModel) bool {
	changed := false
	for i := len(state.Networks); i < len(plan.Networks); i++ {
		n := &plan.Networks[i]
		if n.Model.IsNull() || n.Model.IsUnknown() {
			n.Model, changed = types.StringValue("virtio"), true
		}
		if n.Firewall.IsNull() {
			n.Firewall, changed = types.BoolUnknown(), true
		}
		if n.MACAddress.IsNull() {
			n.MACAddress, changed = types.StringUnknown(), true
		}
	}
	if ci := plan.CloudInit; ci != nil && state.CloudInit == nil {
		for _, v := range []*types.String{&ci.User, &ci.IP, &ci.Gateway, &ci.Nameserver, &ci.SearchDomain, &ci.IPAddress} {
			if v.IsNull() {
				*v, changed = types.StringUnknown(), true
			}
		}
		if ci.SSHKeys.IsNull() {
			ci.SSHKeys, changed = types.ListUnknown(types.StringType), true
		}
	}
	return changed
}

// addressMayChange tells whether the address the VM gets can differ after
// this update, so ip_address must be unknown in the plan.
func addressMayChange(state, plan vmModel) bool {
	if plan.CloudInit == nil {
		return false
	}
	if state.CloudInit == nil || !plan.CloudInit.IP.Equal(state.CloudInit.IP) {
		return true
	}
	if len(plan.Networks) == 0 || len(state.Networks) == 0 {
		return false
	}
	return !plan.Networks[0].Bridge.Equal(state.Networks[0].Bridge)
}

func planKnown(m vmModel) bool {
	for _, d := range m.Disks {
		if d.Slot.IsUnknown() || d.Storage.IsUnknown() || d.SizeGB.IsUnknown() {
			return false
		}
	}
	return true
}

// modelToVM is the described part of the model, in vmconfig terms.
func modelToVM(m vmModel) vmconfig.VM {
	vm := vmconfig.VM{Name: m.Name.ValueString(), Cores: m.Cores.ValueInt64(), Sockets: m.Sockets.ValueInt64(), MemoryMB: m.MemoryMB.ValueInt64()}
	if !m.CPU.IsUnknown() {
		vm.CPU = m.CPU.ValueString()
	}
	for _, d := range m.Disks {
		vm.Disks = append(vm.Disks, vmconfig.Disk{Slot: d.Slot.ValueString(), Storage: d.Storage.ValueString(), SizeGB: d.SizeGB.ValueInt64()})
	}
	for i, n := range m.Networks {
		nic := vmconfig.NIC{Slot: n.Slot.ValueString(), Bridge: n.Bridge.ValueString(), Model: n.Model.ValueString()}
		if nic.Slot == "" {
			nic.Slot = "net" + strconv.Itoa(i)
		}
		if !n.VLAN.IsNull() && !n.VLAN.IsUnknown() {
			v := n.VLAN.ValueInt64()
			nic.VLAN = &v
		}
		if !n.Firewall.IsNull() && !n.Firewall.IsUnknown() {
			b := n.Firewall.ValueBool()
			nic.Firewall = &b
		}
		if !n.MACAddress.IsUnknown() {
			nic.MAC = n.MACAddress.ValueString()
		}
		vm.NICs = append(vm.NICs, nic)
	}
	if ci := m.CloudInit; ci != nil {
		// Unknown (unset, computed) fields read as empty: not described.
		known := func(v types.String) string {
			if v.IsUnknown() {
				return ""
			}
			return v.ValueString()
		}
		out := &vmconfig.CloudInit{User: known(ci.User), Gateway: known(ci.Gateway), Nameserver: known(ci.Nameserver), SearchDomain: known(ci.SearchDomain), IP: known(ci.IP)}
		if !ci.SSHKeys.IsNull() && !ci.SSHKeys.IsUnknown() {
			out.SSHKeys = append([]string{}, normalizeKeys(ci.SSHKeys)...)
		}
		vm.CloudInit = out
	}
	return vm
}

func vmID(conn string, vmid int64) string { return fmt.Sprintf("%s/%d", conn, vmid) }

func parseVMID(id string) (string, int, error) {
	i := strings.LastIndex(id, "/")
	if i <= 0 {
		return "", 0, fmt.Errorf("import id %q is not <connection_id>/<vmid>", id)
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n <= 0 {
		return "", 0, fmt.Errorf("import id %q is not <connection_id>/<vmid>", id)
	}
	return id[:i], n, nil
}

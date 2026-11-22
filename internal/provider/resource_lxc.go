package provider

import (
	"context"
	"regexp"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/adminsyspro/terraform-provider-proxcenter/internal/vmconfig"
)

var (
	_ resource.Resource                   = (*lxcResource)(nil)
	_ resource.ResourceWithConfigure      = (*lxcResource)(nil)
	_ resource.ResourceWithImportState    = (*lxcResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*lxcResource)(nil)
	_ resource.ResourceWithValidateConfig = (*lxcResource)(nil)
)

type ctSourceModel struct {
	OSTemplate types.String `tfsdk:"ostemplate"`
	CloneOf    types.Int64  `tfsdk:"clone_of"`
}

type rootfsModel struct {
	Storage types.String `tfsdk:"storage"`
	SizeGB  types.Int64  `tfsdk:"size_gb"`
}

type mountPointModel struct {
	Slot    types.String `tfsdk:"slot"`
	Storage types.String `tfsdk:"storage"`
	SizeGB  types.Int64  `tfsdk:"size_gb"`
	Path    types.String `tfsdk:"path"`
}

type ctNetworkModel struct {
	Slot       types.String `tfsdk:"slot"`
	Name       types.String `tfsdk:"name"`
	Bridge     types.String `tfsdk:"bridge"`
	VLAN       types.Int64  `tfsdk:"vlan"`
	IP         types.String `tfsdk:"ip"`
	Gateway    types.String `tfsdk:"gateway"`
	Firewall   types.Bool   `tfsdk:"firewall"`
	MACAddress types.String `tfsdk:"mac_address"`
}

type lxcModel struct {
	ID             types.String      `tfsdk:"id"`
	ConnectionID   types.String      `tfsdk:"connection_id"`
	VMID           types.Int64       `tfsdk:"vmid"`
	Node           types.String      `tfsdk:"node"`
	TargetNode     types.String      `tfsdk:"target_node"`
	Hostname       types.String      `tfsdk:"hostname"`
	Source         *ctSourceModel    `tfsdk:"source"`
	Cores          types.Int64       `tfsdk:"cores"`
	MemoryMB       types.Int64       `tfsdk:"memory_mb"`
	SwapMB         types.Int64       `tfsdk:"swap_mb"`
	Unprivileged   types.Bool        `tfsdk:"unprivileged"`
	Features       types.Map         `tfsdk:"features"`
	RootFS         *rootfsModel      `tfsdk:"rootfs"`
	MountPoints    []mountPointModel `tfsdk:"mount_point"`
	Networks       []ctNetworkModel  `tfsdk:"network"`
	SSHKeys        types.List        `tfsdk:"ssh_keys"`
	PasswordWO     types.String      `tfsdk:"password_wo"`
	Nameserver     types.String      `tfsdk:"nameserver"`
	SearchDomain   types.String      `tfsdk:"search_domain"`
	PowerState     types.String      `tfsdk:"power_state"`
	RebootOnChange types.Bool        `tfsdk:"reboot_on_change"`
	ForceStop      types.Bool        `tfsdk:"force_stop"`
	Timeouts       timeouts.Value    `tfsdk:"timeouts"`
}

type lxcResource struct {
	clientHolder
}

func newLXCResource() resource.Resource { return &lxcResource{} }

func (r *lxcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lxc"
}

func (r *lxcResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if err := r.configureFrom(req.ProviderData); err != nil {
		resp.Diagnostics.AddError("Provider misconfigured", err.Error())
	}
}

var mpSlotRe = regexp.MustCompile(`^mp\d+$`)

func (r *lxcResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An LXC container managed through ProxCenter, created from a container template or by cloning a container. " +
			"Quotas, allowed networks and vDC rules are enforced by ProxCenter; its refusals are shown as they are.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "<connection_id>/<vmid>, also the import id.", PlanModifiers: keep},
			"connection_id": schema.StringAttribute{Required: true, Description: "Cluster connection, e.g. data.proxcenter_vdc.x.connection_id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"vmid": schema.Int64Attribute{Optional: true, Computed: true, Description: "Unset: the next free VMID (inside the tenant range for a vDC tenant).",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace(), int64planmodifier.UseStateForUnknown()}},
			"node":        schema.StringAttribute{Computed: true, Description: "Node the container runs on, read back on every refresh.", PlanModifiers: keep},
			"target_node": schema.StringAttribute{Optional: true, Computed: true, Description: "Node to create the container on; required for an ostemplate source, defaults to the source's node for a clone. Ignored after creation.", PlanModifiers: keep},
			"hostname":    schema.StringAttribute{Required: true, Description: "Hostname of the container."},
			"cores":       schema.Int64Attribute{Required: true, Description: "CPU cores."},
			"memory_mb":   schema.Int64Attribute{Required: true, Description: "Memory, MiB."},
			"swap_mb": schema.Int64Attribute{Optional: true, Computed: true, Description: "Swap, MiB; 512 at creation when unset.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"unprivileged": schema.BoolAttribute{Optional: true, Computed: true, Description: "Unprivileged container; true at creation when unset. Set at creation only; ProxCenter refuses false to a vDC tenant.",
				PlanModifiers: []planmodifier.Bool{boolUseStateForUnknown()}},
			"features": schema.MapAttribute{Optional: true, Computed: true, ElementType: types.BoolType, Description: "Container features, e.g. { nesting = true }; unset keeps what the container has.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()}},
			"ssh_keys": schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "Public SSH keys for root, set at creation only.",
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
			"password_wo":   schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, Description: "Root password, set at creation only and never stored in state (Terraform 1.11+)."},
			"nameserver":    schema.StringAttribute{Optional: true, Computed: true, Description: "DNS server, set at creation only.", PlanModifiers: keep},
			"search_domain": schema.StringAttribute{Optional: true, Computed: true, Description: "DNS search domain, set at creation only.", PlanModifiers: keep},
			"power_state": schema.StringAttribute{Optional: true, Computed: true, Description: "running or stopped; running at creation when unset, the container's state otherwise.",
				Validators: []validator.String{stringvalidator.OneOf("running", "stopped")}, PlanModifiers: keep},
			"reboot_on_change": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Reboot the container when a change waits for a reboot; false only warns."},
			"force_stop": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Hard stop the container when a clean shutdown does not stop it."},
		},
		Blocks: map[string]schema.Block{
			"source": schema.SingleNestedBlock{
				Description: "Where the container comes from; required to create it, optional after an import. Changing it replaces the container.",
				Attributes: map[string]schema.Attribute{
					"ostemplate": schema.StringAttribute{Optional: true, Description: "Container template volume, e.g. local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst."},
					"clone_of":   schema.Int64Attribute{Optional: true, Description: "VMID of the container or container template to clone."},
				},
				PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplaceIf(sourceChangeReplaces,
					"A different source replaces the container.", "A different source replaces the container.")},
			},
			"rootfs": schema.SingleNestedBlock{
				Description: "Root filesystem. It grows in place and moves when its storage changes; shrinking it is refused at plan.",
				Attributes: map[string]schema.Attribute{
					"storage": schema.StringAttribute{Optional: true, Description: "Storage of the root filesystem."},
					"size_gb": schema.Int64Attribute{Optional: true, Description: "Size, GiB; it may only grow."},
				},
			},
			"mount_point": schema.ListNestedBlock{
				Description: "Mount points by slot, set at creation; they grow and move in place. Adding or removing one after creation is refused at plan.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"slot":    schema.StringAttribute{Required: true, Description: "mp0, mp1..."},
					"storage": schema.StringAttribute{Required: true, Description: "Storage of the volume."},
					"size_gb": schema.Int64Attribute{Required: true, Description: "Size, GiB; it may only grow."},
					"path":    schema.StringAttribute{Required: true, Description: "Mount path inside the container, e.g. /data."},
				}},
			},
			"network": schema.ListNestedBlock{
				Description: "Network cards by slot. Set slot on each block when there are several. A card no longer described is refused at plan.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"slot":        schema.StringAttribute{Optional: true, Computed: true, Description: "net0, net1...; defaults to net<index>.", PlanModifiers: keep},
					"name":        schema.StringAttribute{Optional: true, Computed: true, Description: "Interface name in the container; eth<index> when unset.", PlanModifiers: keep},
					"bridge":      schema.StringAttribute{Required: true, Description: "Bridge or VNet (pve_name of data.proxcenter_vnets)."},
					"vlan":        schema.Int64Attribute{Optional: true, Description: "VLAN tag; a vDC tenant token needs vms:nic."},
					"ip":          schema.StringAttribute{Optional: true, Computed: true, Description: "dhcp or an address in CIDR form. ProxCenter allocates no IPAM address to a container.", PlanModifiers: keep},
					"gateway":     schema.StringAttribute{Optional: true, Computed: true, Description: "Gateway of a static address; unset keeps what is set.", PlanModifiers: keep},
					"firewall":    schema.BoolAttribute{Optional: true, Computed: true, Description: "Proxmox firewall on the card; unset keeps what is set.", PlanModifiers: []planmodifier.Bool{boolUseStateForUnknown()}},
					"mac_address": schema.StringAttribute{Optional: true, Computed: true, Description: "A vDC tenant token needs vms:nic to set it.", PlanModifiers: keep},
				}},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *lxcResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m lxcModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.RootFS == nil || m.RootFS.Storage.IsNull() || m.RootFS.SizeGB.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("rootfs"), "Missing rootfs", "A rootfs block with storage and size_gb is required.")
	}
	if len(m.Networks) > 1 {
		for _, n := range m.Networks {
			if n.Slot.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root("network"), "Missing network slot",
					"Set slot (net0, net1...) on every network block when there are several network blocks: a list position is not a card's identity.")
				break
			}
		}
	}
	for _, mp := range m.MountPoints {
		if !mp.Slot.IsUnknown() && !mpSlotRe.MatchString(mp.Slot.ValueString()) {
			resp.Diagnostics.AddAttributeError(path.Root("mount_point"), "Invalid mount point slot",
				"A mount point slot is mp0, mp1... (got "+strconv.Quote(mp.Slot.ValueString())+").")
		}
	}
	if src := m.Source; src != nil && !src.OSTemplate.IsUnknown() && !src.CloneOf.IsUnknown() {
		if src.OSTemplate.IsNull() == src.CloneOf.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("source"), "Invalid source", "Set exactly one of ostemplate or clone_of.")
		}
	}
}

func (r *lxcResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan lxcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for i := range plan.Networks {
		n := &plan.Networks[i]
		if n.Slot.IsUnknown() || n.Slot.IsNull() {
			n.Slot = types.StringValue("net" + strconv.Itoa(i))
		}
		if n.Name.IsUnknown() || n.Name.IsNull() {
			n.Name = types.StringValue("eth" + strconv.Itoa(i))
		}
	}
	if req.State.Raw.IsNull() {
		var targetNode types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("target_node"), &targetNode)...)
		clone := plan.Source != nil && !plan.Source.CloneOf.IsNull()
		if plan.Source == nil {
			resp.Diagnostics.AddAttributeError(path.Root("source"), "Missing source",
				"source is required to create a container: set ostemplate or clone_of.")
		} else if !clone && targetNode.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("target_node"), "Missing target_node",
				"A container from a template needs target_node: pick one of the nodes of its vDC (data.proxcenter_vdc.<name>.nodes).")
		}
		if clone {
			// A clone takes these from its source, and the API cannot set them
			// once the container exists.
			for _, name := range []string{"password_wo", "ssh_keys", "nameserver", "search_domain", "unprivileged"} {
				var v attr.Value
				resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(name), &v)...)
				if v != nil && !v.IsNull() {
					resp.Diagnostics.AddAttributeError(path.Root(name), "Not settable on a clone",
						name+" of a cloned container comes from its source and cannot be set afterwards: set it in the source, or inside the container.")
				}
			}
		} else {
			if plan.SwapMB.IsUnknown() {
				plan.SwapMB = types.Int64Value(512)
			}
			if plan.Unprivileged.IsUnknown() {
				plan.Unprivileged = types.BoolValue(true)
			}
		}
		if plan.PowerState.IsUnknown() {
			plan.PowerState = types.StringValue("running")
		}
	} else {
		var state lxcModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.TargetNode = state.TargetNode
		if _, err := vmconfig.CTChanges(modelToCT(state), modelToCT(plan), nil); err != nil {
			resp.Diagnostics.AddError("Change refused", err.Error())
		}
		for i := len(state.Networks); i < len(plan.Networks); i++ {
			n := &plan.Networks[i]
			if n.Firewall.IsNull() {
				n.Firewall = types.BoolUnknown()
			}
			if n.MACAddress.IsNull() {
				n.MACAddress = types.StringUnknown()
			}
			if n.IP.IsNull() {
				n.IP = types.StringUnknown()
			}
			if n.Gateway.IsNull() {
				n.Gateway = types.StringUnknown()
			}
		}
	}
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	}
}

func joinWords(w []string) string {
	out := ""
	for i, s := range w {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// modelToCT is the described part of the model, in vmconfig terms.
func modelToCT(m lxcModel) vmconfig.CT {
	known := func(v types.String) string {
		if v.IsUnknown() {
			return ""
		}
		return v.ValueString()
	}
	ct := vmconfig.CT{Hostname: m.Hostname.ValueString(), Cores: m.Cores.ValueInt64(), MemoryMB: m.MemoryMB.ValueInt64()}
	if !m.SwapMB.IsUnknown() && !m.SwapMB.IsNull() {
		v := m.SwapMB.ValueInt64()
		ct.SwapMB = &v
	}
	if !m.Features.IsNull() && !m.Features.IsUnknown() {
		ct.Features = map[string]bool{}
		for k, v := range m.Features.Elements() {
			if b, ok := v.(types.Bool); ok {
				ct.Features[k] = b.ValueBool()
			}
		}
	}
	if m.RootFS != nil {
		ct.RootFS = vmconfig.MountPoint{Slot: "rootfs", Storage: m.RootFS.Storage.ValueString(), SizeGB: m.RootFS.SizeGB.ValueInt64()}
	}
	for _, mp := range m.MountPoints {
		ct.MountPoints = append(ct.MountPoints, vmconfig.MountPoint{Slot: mp.Slot.ValueString(), Storage: mp.Storage.ValueString(), Path: mp.Path.ValueString(), SizeGB: mp.SizeGB.ValueInt64()})
	}
	for i, n := range m.Networks {
		nic := vmconfig.CTNIC{Slot: known(n.Slot), Name: known(n.Name), Bridge: n.Bridge.ValueString(), IP: known(n.IP), Gateway: known(n.Gateway), MAC: known(n.MACAddress)}
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
		ct.NICs = append(ct.NICs, nic)
	}
	return ct
}

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type storageModel struct {
	Storage types.String `tfsdk:"storage"`
	Type    types.String `tfsdk:"type"`
	Shared  types.Bool   `tfsdk:"shared"`
	Nodes   types.List   `tfsdk:"nodes"`
	Content types.List   `tfsdk:"content"`
	Free    types.Int64  `tfsdk:"free"`
	Total   types.Int64  `tfsdk:"total"`
}

type storagesDataSource struct{ clientHolder }

func newStoragesDataSource() datasource.DataSource { return &storagesDataSource{} }

func (d *storagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storages"
}

func (d *storagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Storages of a connection; a vDC tenant sees its vDC storages only. Needs storage:read.",
		Attributes: map[string]schema.Attribute{
			"connection_id": schema.StringAttribute{Required: true},
			"storages": schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"storage": schema.StringAttribute{Computed: true},
				"type":    schema.StringAttribute{Computed: true},
				"shared":  schema.BoolAttribute{Computed: true},
				"nodes":   computedStrings(""),
				"content": computedStrings(""),
				"free":    schema.Int64Attribute{Computed: true, Description: "Bytes."},
				"total":   schema.Int64Attribute{Computed: true, Description: "Bytes."},
			}}},
		},
	}
}

func (d *storagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg struct {
		ConnectionID types.String   `tfsdk:"connection_id"`
		Storages     []storageModel `tfsdk:"storages"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := d.client.Storages(ctx, cfg.ConnectionID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading the storages failed", err.Error())
		return
	}
	cfg.Storages = []storageModel{}
	for _, s := range list {
		cfg.Storages = append(cfg.Storages, storageModel{types.StringValue(s.Storage), types.StringValue(s.Type), types.BoolValue(s.Shared),
			stringList(s.Nodes), stringList(s.Content), types.Int64Value(s.Free), types.Int64Value(s.Total)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

type vnetModel struct {
	Name        types.String `tfsdk:"name"`
	PVEName     types.String `tfsdk:"pve_name"`
	Bridge      types.String `tfsdk:"bridge"`
	Tag         types.Int64  `tfsdk:"tag"`
	Type        types.String `tfsdk:"type"`
	CIDR        types.String `tfsdk:"cidr"`
	Gateway     types.String `tfsdk:"gateway"`
	IPAMEnabled types.Bool   `tfsdk:"ipam_enabled"`
}

type vnetsDataSource struct{ clientHolder }

func newVnetsDataSource() datasource.DataSource { return &vnetsDataSource{} }

func (d *vnetsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vnets"
}

func (d *vnetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := schema.StringAttribute{Computed: true}
	resp.Schema = schema.Schema{
		Description: "VNets of a vDC; pve_name is the bridge of a proxcenter_vm network block. The token needs a scope that opens GET /api/v1/vdcs/{id}/vnets (vdcs:read, ipam:read, nodes:read or network:write) besides its VM scopes.",
		Attributes: map[string]schema.Attribute{
			"vdc_id": schema.StringAttribute{Required: true},
			"vnets": schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"name": str, "pve_name": str, "bridge": str, "tag": schema.Int64Attribute{Computed: true}, "type": str,
				"cidr": str, "gateway": str, "ipam_enabled": schema.BoolAttribute{Computed: true},
			}}},
		},
	}
}

func (d *vnetsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg struct {
		VDCID types.String `tfsdk:"vdc_id"`
		VNets []vnetModel  `tfsdk:"vnets"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := d.client.VNets(ctx, cfg.VDCID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading the VNets failed", err.Error())
		return
	}
	cfg.VNets = []vnetModel{}
	for _, v := range list {
		m := vnetModel{Name: types.StringValue(v.DisplayName), PVEName: types.StringValue(v.PVEName), Bridge: optionalString(v.Bridge),
			Tag: types.Int64Value(v.Tag), Type: types.StringValue(v.Type), CIDR: types.StringNull(), Gateway: types.StringNull(), IPAMEnabled: types.BoolValue(false)}
		if v.Subnet != nil {
			m.CIDR, m.Gateway, m.IPAMEnabled = types.StringValue(v.Subnet.CIDR), optionalString(v.Subnet.Gateway), types.BoolValue(v.Subnet.IPAMEnabled)
		}
		cfg.VNets = append(cfg.VNets, m)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

type vmSummaryModel struct {
	VMID     types.Int64  `tfsdk:"vmid"`
	Name     types.String `tfsdk:"name"`
	Node     types.String `tfsdk:"node"`
	Status   types.String `tfsdk:"status"`
	Template types.Bool   `tfsdk:"template"`
}

type vmsDataSource struct{ clientHolder }

func newVmsDataSource() datasource.DataSource { return &vmsDataSource{} }

func (d *vmsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vms"
}

func (d *vmsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := schema.StringAttribute{Computed: true}
	resp.Schema = schema.Schema{
		Description: "QEMU VMs of a connection the token sees, e.g. to find a template for source.clone_of. Needs vms:read.",
		Attributes: map[string]schema.Attribute{
			"connection_id": schema.StringAttribute{Required: true},
			"vmids":         schema.ListAttribute{Optional: true, ElementType: types.Int64Type, Description: "Only these VMIDs."},
			"vms": schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"vmid": schema.Int64Attribute{Computed: true}, "name": str, "node": str, "status": str, "template": schema.BoolAttribute{Computed: true},
			}}},
		},
	}
}

func (d *vmsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg struct {
		ConnectionID types.String     `tfsdk:"connection_id"`
		VMIDs        []int64          `tfsdk:"vmids"`
		VMs          []vmSummaryModel `tfsdk:"vms"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ids := make([]int, len(cfg.VMIDs))
	for i, id := range cfg.VMIDs {
		ids[i] = int(id)
	}
	list, err := d.client.ListVMs(ctx, cfg.ConnectionID.ValueString(), ids)
	if err != nil {
		resp.Diagnostics.AddError("Reading the VMs failed", err.Error())
		return
	}
	cfg.VMs = []vmSummaryModel{}
	for _, vm := range list {
		if vm.Type != "" && vm.Type != "qemu" {
			continue
		}
		cfg.VMs = append(cfg.VMs, vmSummaryModel{types.Int64Value(int64(vm.VMID)), types.StringValue(vm.Name), types.StringValue(vm.Node), types.StringValue(vm.Status), types.BoolValue(vm.Template)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

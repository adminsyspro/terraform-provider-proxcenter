package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/adminsyspro/terraform-provider-proxcenter/internal/client"
)

type vdcQuotaModel struct {
	MaxVcpus     types.Int64 `tfsdk:"max_vcpus"`
	MaxRAMMB     types.Int64 `tfsdk:"max_ram_mb"`
	MaxStorageMB types.Int64 `tfsdk:"max_storage_mb"`
	MaxVMs       types.Int64 `tfsdk:"max_vms"`
}

type vdcUsageModel struct {
	UsedVcpus     types.Int64 `tfsdk:"used_vcpus"`
	UsedRAMMB     types.Int64 `tfsdk:"used_ram_mb"`
	UsedStorageMB types.Int64 `tfsdk:"used_storage_mb"`
	UsedVMs       types.Int64 `tfsdk:"used_vms"`
}

type vdcModel struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	TenantID       types.String   `tfsdk:"tenant_id"`
	ConnectionID   types.String   `tfsdk:"connection_id"`
	Pool           types.String   `tfsdk:"pool"`
	Nodes          types.List     `tfsdk:"nodes"`
	Storages       types.List     `tfsdk:"storages"`
	PrimaryStorage types.String   `tfsdk:"primary_storage"`
	Quota          *vdcQuotaModel `tfsdk:"quota"`
	Usage          *vdcUsageModel `tfsdk:"usage"`
}

func vdcToModel(v client.VDC) vdcModel {
	m := vdcModel{
		ID: types.StringValue(v.ID), Name: types.StringValue(v.Name), TenantID: types.StringValue(v.TenantID),
		ConnectionID: types.StringValue(v.ConnectionID), Pool: types.StringValue(v.PVEPoolName),
		Nodes: stringList(v.Nodes), Storages: stringList(v.Storages), PrimaryStorage: optionalString(v.PrimaryStorage),
	}
	if v.Quota != nil {
		m.Quota = &vdcQuotaModel{optionalInt(v.Quota.MaxVcpus), optionalInt(v.Quota.MaxRAMMB), optionalInt(v.Quota.MaxStorageMB), optionalInt(v.Quota.MaxVMs)}
	}
	if v.Usage != nil {
		m.Usage = &vdcUsageModel{types.Int64Value(v.Usage.UsedVcpus), types.Int64Value(v.Usage.UsedRAMMB), types.Int64Value(v.Usage.UsedStorageMB), types.Int64Value(v.Usage.UsedVMs)}
	}
	return m
}

func vdcAttributes(lookup bool) map[string]schema.Attribute {
	intAttr := schema.Int64Attribute{Computed: true}
	attrs := map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: !lookup, Optional: lookup, Description: "vDC id."},
		"name":            schema.StringAttribute{Computed: !lookup, Optional: lookup, Description: "vDC name."},
		"tenant_id":       schema.StringAttribute{Computed: true},
		"connection_id":   schema.StringAttribute{Computed: true, Description: "Cluster connection the vDC lives on; the connection_id of its VMs."},
		"pool":            schema.StringAttribute{Computed: true, Description: "Proxmox resource pool of the vDC."},
		"nodes":           computedStrings("Nodes the vDC may place VMs on; target_node of a proxcenter_vm."),
		"storages":        computedStrings("Storages the vDC may use."),
		"primary_storage": schema.StringAttribute{Computed: true},
		"quota": schema.SingleNestedAttribute{Computed: true, Description: "Quota; a null limit is unlimited.", Attributes: map[string]schema.Attribute{
			"max_vcpus": intAttr, "max_ram_mb": intAttr, "max_storage_mb": intAttr, "max_vms": intAttr,
		}},
		"usage": schema.SingleNestedAttribute{Computed: true, Description: "Usage computed by ProxCenter.", Attributes: map[string]schema.Attribute{
			"used_vcpus": intAttr, "used_ram_mb": intAttr, "used_storage_mb": intAttr, "used_vms": intAttr,
		}},
	}
	if lookup {
		attrs["id"] = schema.StringAttribute{Optional: true, Computed: true, Description: "vDC id. Set id or name."}
		attrs["name"] = schema.StringAttribute{Optional: true, Computed: true, Description: "vDC name. Set id or name."}
	}
	return attrs
}

type vdcDataSource struct{ clientHolder }

func newVdcDataSource() datasource.DataSource { return &vdcDataSource{} }

func (d *vdcDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vdc"
}

func (d *vdcDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "One vDC the token sees, with its nodes, storages, quota and usage. Needs vms:read or vdcs:read.",
		Attributes:  vdcAttributes(true),
	}
}

func (d *vdcDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *vdcDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg vdcModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vdcs, err := d.client.ListVDCs(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Reading the vDCs failed", err.Error())
		return
	}
	names := make([]string, 0, len(vdcs))
	for _, v := range vdcs {
		if (!cfg.ID.IsNull() && v.ID == cfg.ID.ValueString()) || (!cfg.Name.IsNull() && v.Name == cfg.Name.ValueString()) {
			m := vdcToModel(v)
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
		names = append(names, v.Name)
	}
	sort.Strings(names)
	what := fmt.Sprintf("No vDC named %q", cfg.Name.ValueString())
	if !cfg.ID.IsNull() {
		what = fmt.Sprintf("No vDC with id %q", cfg.ID.ValueString())
	}
	resp.Diagnostics.AddError("vDC not found", fmt.Sprintf("%s among the vDCs this token sees: %s.", what, strings.Join(names, ", ")))
}

type vdcsDataSource struct{ clientHolder }

func newVdcsDataSource() datasource.DataSource { return &vdcsDataSource{} }

func (d *vdcsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vdcs"
}

func (d *vdcsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Every vDC the token sees. Needs vms:read or vdcs:read.",
		Attributes: map[string]schema.Attribute{
			"vdcs": schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: vdcAttributes(false)}},
		},
	}
}

func (d *vdcsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	vdcs, err := d.client.ListVDCs(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Reading the vDCs failed", err.Error())
		return
	}
	out := struct {
		VDCs []vdcModel `tfsdk:"vdcs"`
	}{VDCs: make([]vdcModel, 0, len(vdcs))}
	for _, v := range vdcs {
		out.VDCs = append(out.VDCs, vdcToModel(v))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

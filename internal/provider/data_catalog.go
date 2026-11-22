package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type imageModel struct {
	Slug     types.String `tfsdk:"slug"`
	Name     types.String `tfsdk:"name"`
	Vendor   types.String `tfsdk:"vendor"`
	Version  types.String `tfsdk:"version"`
	Format   types.String `tfsdk:"format"`
	IsCustom types.Bool   `tfsdk:"is_custom"`
}

type imagesDataSource struct{ clientHolder }

func newImagesDataSource() datasource.DataSource { return &imagesDataSource{} }

func (d *imagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Catalogue images the token may deploy (source.image of a proxcenter_vm). Needs a VM scope.",
		Attributes: map[string]schema.Attribute{
			"vendor": schema.StringAttribute{Optional: true, Description: "Only the images of this vendor."},
			"images": schema.ListNestedAttribute{Computed: true, Description: "The images, built-in and custom.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"slug":      cStr("Identifier to use as source.image."),
				"name":      cStr("Display name."),
				"vendor":    cStr("Distribution or vendor, e.g. debian."),
				"version":   cStr("Version of the image."),
				"format":    cStr("qcow2, raw, img or iso."),
				"is_custom": cBool("Whether the image was added in ProxCenter rather than built in."),
			}}},
		},
	}
}

func (d *imagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg struct {
		Vendor types.String `tfsdk:"vendor"`
		Images []imageModel `tfsdk:"images"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	images, err := d.client.Catalog(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Reading the catalogue failed", err.Error())
		return
	}
	cfg.Images = []imageModel{}
	for _, im := range images {
		if !cfg.Vendor.IsNull() && im.Vendor != cfg.Vendor.ValueString() {
			continue
		}
		cfg.Images = append(cfg.Images, imageModel{types.StringValue(im.Slug), types.StringValue(im.Name), types.StringValue(im.Vendor),
			types.StringValue(im.Version), types.StringValue(im.Format), types.BoolValue(im.IsCustom)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

type blueprintModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	ImageSlug   types.String `tfsdk:"image_slug"`
	Description types.String `tfsdk:"description"`
}

type blueprintsDataSource struct{ clientHolder }

func newBlueprintsDataSource() datasource.DataSource { return &blueprintsDataSource{} }

func (d *blueprintsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_blueprints"
}

func (d *blueprintsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Blueprints the token may deploy (source.blueprint of a proxcenter_vm). Needs a VM scope.",
		Attributes: map[string]schema.Attribute{
			"blueprints": schema.ListNestedAttribute{Computed: true, Description: "The blueprints.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"id":          cStr("Identifier to use as source.blueprint."),
				"name":        cStr("Display name."),
				"image_slug":  cStr("Catalogue image the blueprint deploys."),
				"description": cStr("Description of the blueprint."),
			}}},
		},
	}
}

func (d *blueprintsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	bps, err := d.client.Blueprints(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Reading the blueprints failed", err.Error())
		return
	}
	out := struct {
		Blueprints []blueprintModel `tfsdk:"blueprints"`
	}{Blueprints: []blueprintModel{}}
	for _, bp := range bps {
		out.Blueprints = append(out.Blueprints, blueprintModel{types.StringValue(bp.ID), types.StringValue(bp.Name), types.StringValue(bp.ImageSlug), optionalString(bp.Description)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

// Package provider implements the ProxCenter Terraform provider.
package provider

import (
	"context"
	"os"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/adminsyspro/terraform-provider-proxcenter/internal/client"
)

var _ provider.Provider = (*proxcenterProvider)(nil)

type proxcenterProvider struct {
	version string
}

type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Token    types.String `tfsdk:"token"`
	Insecure types.Bool   `tfsdk:"insecure_skip_verify"`
}

// New returns the provider factory for a build version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &proxcenterProvider{version: version} }
}

func (p *proxcenterProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "proxcenter"
	resp.Version = p.version
}

func (p *proxcenterProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages ProxCenter through its public API v1 with a pxc_ API token.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "ProxCenter URL, e.g. https://proxcenter.example.com. Defaults to PROXCENTER_ENDPOINT.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "ProxCenter API token (pxc_...). Defaults to PROXCENTER_TOKEN.",
			},
			"insecure_skip_verify": schema.BoolAttribute{
				Optional:    true,
				Description: "Skip TLS certificate verification. Defaults to PROXCENTER_INSECURE, else false.",
			},
		},
	}
}

func (p *proxcenterProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	endpoint := stringOrEnv(cfg.Endpoint, "PROXCENTER_ENDPOINT")
	token := stringOrEnv(cfg.Token, "PROXCENTER_TOKEN")
	insecure := false
	if !cfg.Insecure.IsNull() && !cfg.Insecure.IsUnknown() {
		insecure = cfg.Insecure.ValueBool()
	} else if v, err := strconv.ParseBool(os.Getenv("PROXCENTER_INSECURE")); err == nil {
		insecure = v
	}
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing ProxCenter endpoint",
			"Set endpoint in the provider block or the PROXCENTER_ENDPOINT environment variable.")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing ProxCenter API token",
			"Set token in the provider block or the PROXCENTER_TOKEN environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	c, err := client.New(client.Config{
		Endpoint:  endpoint,
		Token:     token,
		Insecure:  insecure,
		UserAgent: "terraform-provider-proxcenter/" + p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid ProxCenter configuration", err.Error())
		return
	}
	if _, err := c.CheckVersion(ctx); err != nil {
		resp.Diagnostics.AddError("Unsupported ProxCenter server", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *proxcenterProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *proxcenterProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newVdcDataSource, newVdcsDataSource, newImagesDataSource, newBlueprintsDataSource,
		newStoragesDataSource, newVnetsDataSource, newVmsDataSource,
	}
}

func stringOrEnv(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	return os.Getenv(env)
}

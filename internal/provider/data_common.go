package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/adminsyspro/terraform-provider-proxcenter/internal/client"
)

// clientHolder hands the configured client to data sources and resources.
type clientHolder struct {
	client *client.Client
}

func (h *clientHolder) configureFrom(data any) error {
	if data == nil {
		return nil // provider not configured yet (validation phase)
	}
	c, ok := data.(*client.Client)
	if !ok {
		return fmt.Errorf("unexpected provider data %T", data)
	}
	h.client = c
	return nil
}

func (h *clientHolder) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if err := h.configureFrom(req.ProviderData); err != nil {
		resp.Diagnostics.AddError("Provider misconfigured", err.Error())
	}
}

func stringList(values []string) types.List {
	elems := make([]types.String, len(values))
	for i, v := range values {
		elems[i] = types.StringValue(v)
	}
	l, _ := types.ListValueFrom(context.Background(), types.StringType, elems)
	return l
}

func optionalInt(v *int64) types.Int64 {
	if v == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*v)
}

func optionalString(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

func computedStrings(desc string) schema.ListAttribute {
	return schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: desc}
}

func cStr(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: desc}
}
func cInt(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{Computed: true, Description: desc}
}
func cBool(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{Computed: true, Description: desc}
}

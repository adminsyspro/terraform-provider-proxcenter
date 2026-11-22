package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func (r *lxcResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Not implemented", "create")
}

func (r *lxcResource) Read(_ context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.AddError("Not implemented", "read")
}

func (r *lxcResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Not implemented", "update")
}

func (r *lxcResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddError("Not implemented", "delete")
}

func (r *lxcResource) ImportState(_ context.Context, _ resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.AddError("Not implemented", "import")
}

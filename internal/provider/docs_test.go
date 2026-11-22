package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// The registry documentation is generated from these descriptions: every
// attribute and block of every schema must carry one.
func TestEverySchemaElementIsDescribed(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	for _, newDS := range p.DataSources(ctx) {
		ds := newDS()
		var meta datasource.MetadataResponse
		ds.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "proxcenter"}, &meta)
		var resp datasource.SchemaResponse
		ds.Schema(ctx, datasource.SchemaRequest{}, &resp)
		if resp.Schema.Description == "" {
			t.Errorf("%s has no description", meta.TypeName)
		}
		checkDataAttrs(t, meta.TypeName, resp.Schema.Attributes)
	}
	for _, newRes := range p.Resources(ctx) {
		r := newRes()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "proxcenter"}, &meta)
		var resp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &resp)
		if resp.Schema.Description == "" {
			t.Errorf("%s has no description", meta.TypeName)
		}
		checkResourceAttrs(t, meta.TypeName, resp.Schema.Attributes)
		for name, b := range resp.Schema.Blocks {
			path := meta.TypeName + "." + name
			switch blk := b.(type) {
			case rschema.SingleNestedBlock:
				if name != "timeouts" && blk.Description == "" {
					t.Errorf("%s has no description", path)
				}
				if name != "timeouts" {
					checkResourceAttrs(t, path, blk.Attributes)
				}
			case rschema.ListNestedBlock:
				if blk.Description == "" {
					t.Errorf("%s has no description", path)
				}
				checkResourceAttrs(t, path, blk.NestedObject.Attributes)
			}
		}
	}
}

func checkDataAttrs(t *testing.T, prefix string, attrs map[string]dschema.Attribute) {
	t.Helper()
	for name, a := range attrs {
		path := prefix + "." + name
		if a.GetDescription() == "" {
			t.Errorf("%s has no description", path)
		}
		switch n := a.(type) {
		case dschema.ListNestedAttribute:
			checkDataAttrs(t, path, n.NestedObject.Attributes)
		case dschema.SingleNestedAttribute:
			checkDataAttrs(t, path, n.Attributes)
		}
	}
}

func checkResourceAttrs(t *testing.T, prefix string, attrs map[string]rschema.Attribute) {
	t.Helper()
	for name, a := range attrs {
		if a.GetDescription() == "" {
			t.Errorf("%s.%s has no description", prefix, name)
		}
	}
}

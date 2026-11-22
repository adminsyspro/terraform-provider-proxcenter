package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func providerSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp provider.SchemaResponse
	New("test")().Schema(context.Background(), provider.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func configure(t *testing.T, endpoint, token *string) provider.ConfigureResponse {
	t.Helper()
	s := providerSchema(t)
	str := func(v *string) tftypes.Value {
		if v == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *v)
	}
	raw := tftypes.NewValue(s.Type().TerraformType(context.Background()), map[string]tftypes.Value{
		"endpoint":             str(endpoint),
		"token":                str(token),
		"insecure_skip_verify": tftypes.NewValue(tftypes.Bool, nil),
	})
	var resp provider.ConfigureResponse
	New("test")().Configure(context.Background(), provider.ConfigureRequest{Config: tfsdk.Config{Raw: raw, Schema: s}}, &resp)
	return resp
}

func fakeHealth(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ProxCenter-Version", "1.5.0")
		_, _ = w.Write([]byte(`{"data":{"status":"ok"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProviderConfigFromEnv(t *testing.T) {
	srv := fakeHealth(t)
	t.Setenv("PROXCENTER_ENDPOINT", srv.URL)
	t.Setenv("PROXCENTER_TOKEN", "pxc_env")
	resp := configure(t, nil, nil)
	if resp.Diagnostics.HasError() {
		t.Fatalf("diagnostics: %v", resp.Diagnostics)
	}
	if resp.ResourceData == nil || resp.DataSourceData == nil {
		t.Fatal("client not handed to resources and data sources")
	}
}

func TestProviderConfigMissingToken(t *testing.T) {
	srv := fakeHealth(t)
	t.Setenv("PROXCENTER_TOKEN", "")
	endpoint := srv.URL
	resp := configure(t, &endpoint, nil)
	if !resp.Diagnostics.HasError() {
		t.Fatal("missing token accepted")
	}
	text := resp.Diagnostics.Errors()[0].Summary() + " " + resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(text, "token") || !strings.Contains(text, "PROXCENTER_TOKEN") {
		t.Fatalf("diagnostic does not name token and PROXCENTER_TOKEN: %q", text)
	}
}

func TestProviderConfigVersionRefusalIsDiagnostic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	endpoint, token := srv.URL, "pxc_x"
	resp := configure(t, &endpoint, &token)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a server without ProxCenter-Version was accepted")
	}
}

func TestProviderSchemaTokenSensitive(t *testing.T) {
	attr, ok := providerSchema(t).Attributes["token"]
	if !ok || !attr.IsSensitive() {
		t.Fatal("token must be a sensitive attribute")
	}
}

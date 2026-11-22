package client

import (
	"encoding/json"
	"os"
	"testing"
)

// usedRoutes lists every route the client calls. Each must exist in the
// OpenAPI document of the ProxCenter version the provider targets, with the
// same operationId, so a renamed or removed route fails here first.
var usedRoutes = []struct{ method, path, operationID string }{
	{"get", "/api/v1/public/health", "public-health"},
	{"get", "/api/v1/tasks/{connectionId}/{node}/{upid}", "task-status"},
	{"get", "/api/v1/connections/{id}/cluster/nextid", "vm-nextid"},
	{"post", "/api/v1/templates/deploy", "template-deploy"},
	{"get", "/api/v1/templates/deployments/{id}", "template-deployment-get"},
	{"post", "/api/v1/connections/{id}/guests/{type}/{node}/{vmid}/clone", "vm-clone"},
	{"get", "/api/v1/connections/{id}/guests/{type}/{node}/{vmid}/config", "vm-config-get"},
	{"put", "/api/v1/connections/{id}/guests/{type}/{node}/{vmid}/config", "vm-config-update"},
	{"post", "/api/v1/connections/{id}/guests/{type}/{node}/{vmid}/disk/resize", "vm-disk-resize"},
	{"post", "/api/v1/connections/{id}/guests/{type}/{node}/{vmid}/disk/move", "vm-disk-move"},
	{"post", "/api/v1/connections/{id}/guests/{type}/{node}/{vmid}/{action}", "vm-power"},
	{"delete", "/api/v1/connections/{id}/guests/{type}/{node}/{vmid}", "vm-delete"},
	{"get", "/api/v1/vms", "vms-list"},
	{"get", "/api/v1/vdcs", "vdcs-list"},
	{"get", "/api/v1/templates/catalog", "templates-catalog"},
	{"get", "/api/v1/templates/blueprints", "templates-blueprints"},
	{"get", "/api/v1/templates/blueprints/{id}", "templates-blueprint-get"},
	{"get", "/api/v1/connections/{id}/storage", "storage-connection-list"},
	{"get", "/api/v1/vdcs/{id}/vnets", "vdc-vnets-list"},
}

func TestRoutesExistInSpec(t *testing.T) {
	raw, err := os.ReadFile("testdata/proxcenter-public-api.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for _, r := range usedRoutes {
		op, ok := spec.Paths[r.path][r.method]
		if !ok {
			t.Errorf("%s %s is not in the public API", r.method, r.path)
			continue
		}
		if op.OperationID != r.operationID {
			t.Errorf("%s %s is %q in the spec, the client expects %q", r.method, r.path, op.OperationID, r.operationID)
		}
	}
}

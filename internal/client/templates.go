package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// DeployHardware is the hardware block of POST /api/v1/templates/deploy.
type DeployHardware struct {
	Cores         int64  `json:"cores"`
	Sockets       int64  `json:"sockets"`
	Memory        int64  `json:"memory"`
	DiskSize      string `json:"diskSize"`
	DiskBus       string `json:"diskBus,omitempty"`
	NetworkBridge string `json:"networkBridge,omitempty"`
	NetworkModel  string `json:"networkModel,omitempty"`
	VLANTag       *int64 `json:"vlanTag,omitempty"`
	CPU           string `json:"cpu,omitempty"`
}

// DeployCloudInit is the cloudInit block of a deployment.
type DeployCloudInit struct {
	CIUser       string `json:"ciuser,omitempty"`
	CIPassword   string `json:"cipassword,omitempty"`
	SSHKeys      string `json:"sshKeys,omitempty"`
	IPConfig0    string `json:"ipconfig0,omitempty"`
	Nameserver   string `json:"nameserver,omitempty"`
	SearchDomain string `json:"searchdomain,omitempty"`
}

// DeployRequest is the body of POST /api/v1/templates/deploy.
type DeployRequest struct {
	ConnectionID string           `json:"connectionId"`
	Node         string           `json:"node"`
	Storage      string           `json:"storage"`
	VMID         int              `json:"vmid"`
	VMName       string           `json:"vmName,omitempty"`
	ImageSlug    string           `json:"imageSlug"`
	BlueprintID  string           `json:"blueprintId,omitempty"`
	Start        bool             `json:"start"`
	Hardware     DeployHardware   `json:"hardware"`
	CloudInit    *DeployCloudInit `json:"cloudInit,omitempty"`
}

// DeployResponse is what POST /api/v1/templates/deploy answers at once.
type DeployResponse struct {
	DeploymentID string `json:"deploymentId"`
	Status       string `json:"status"`
	VMID         int    `json:"vmid"`
}

// Deployment is the progress of a catalogue deployment.
type Deployment struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	CurrentStep  string `json:"currentStep"`
	Error        string `json:"error"`
	ConnectionID string `json:"connectionId"`
	Node         string `json:"node"`
	VMID         int    `json:"vmid"`
}

// Image is a catalogue image.
type Image struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Vendor   string `json:"vendor"`
	Version  string `json:"version"`
	Format   string `json:"format"`
	IsCustom bool   `json:"isCustom"`
}

// Blueprint is a deployment recipe; the API never returns its password.
type Blueprint struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ImageSlug   string `json:"imageSlug"`
}

// Deploy starts a catalogue deployment.
func (c *Client) Deploy(ctx context.Context, req DeployRequest) (DeployResponse, error) {
	var out struct {
		Data DeployResponse `json:"data"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/templates/deploy", nil, req, &out)
	c.invalidate(req.ConnectionID)
	return out.Data, err
}

// GetDeployment reads the progress of a deployment.
func (c *Client) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	var out struct {
		Data Deployment `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/templates/deployments/"+url.PathEscape(id), nil, nil, &out)
	return out.Data, err
}

// Catalog lists the images the caller may deploy.
func (c *Client) Catalog(ctx context.Context) ([]Image, error) {
	var out struct {
		Data struct {
			Images []Image `json:"images"`
		} `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/templates/catalog", nil, nil, &out)
	return out.Data.Images, err
}

// Blueprints lists the blueprints the caller may deploy.
func (c *Client) Blueprints(ctx context.Context) ([]Blueprint, error) {
	var out struct {
		Data []Blueprint `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/templates/blueprints", nil, nil, &out)
	return out.Data, err
}

// Blueprint reads one blueprint.
func (c *Client) Blueprint(ctx context.Context, id string) (Blueprint, error) {
	var out struct {
		Data Blueprint `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/templates/blueprints/"+url.PathEscape(id), nil, nil, &out)
	return out.Data, err
}

// IsVMIDClash tells whether err refuses a VMID another guest already holds.
func IsVMIDClash(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && IsVMIDClashText(apiErr.Message)
}

// IsVMIDClashText tells whether an API or Proxmox message is a VMID clash.
func IsVMIDClashText(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "already exists") || strings.Contains(m, "already in use")
}

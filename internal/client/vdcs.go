package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// VDCQuota limits; nil means unlimited.
type VDCQuota struct {
	MaxVcpus     *int64 `json:"maxVcpus"`
	MaxRAMMB     *int64 `json:"maxRamMb"`
	MaxStorageMB *int64 `json:"maxStorageMb"`
	MaxVMs       *int64 `json:"maxVms"`
}

// VDCUsage is computed by ProxCenter from Proxmox.
type VDCUsage struct {
	UsedVcpus     int64 `json:"usedVcpus"`
	UsedRAMMB     int64 `json:"usedRamMb"`
	UsedStorageMB int64 `json:"usedStorageMb"`
	UsedVMs       int64 `json:"usedVms"`
}

// VDC is one vDC the token sees.
type VDC struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenantId"`
	ConnectionID   string    `json:"connectionId"`
	Name           string    `json:"name"`
	PVEPoolName    string    `json:"pvePoolName"`
	Nodes          []string  `json:"nodes"`
	Storages       []string  `json:"storages"`
	PrimaryStorage string    `json:"primaryStorage"`
	Quota          *VDCQuota `json:"quota"`
	Usage          *VDCUsage `json:"usage"`
}

// Subnet of a VNet; nil for a pure L2 network.
type Subnet struct {
	CIDR        string `json:"cidr"`
	Gateway     string `json:"gateway"`
	IPAMEnabled bool   `json:"ipamEnabled"`
}

// VNet of a vDC.
type VNet struct {
	ID          string  `json:"id"`
	PVEName     string  `json:"pveName"`
	DisplayName string  `json:"displayName"`
	Tag         int64   `json:"tag"`
	Type        string  `json:"type"`
	Bridge      string  `json:"bridge"`
	Subnet      *Subnet `json:"subnet"`
}

// Storage of a cluster.
type Storage struct {
	Storage string   `json:"storage"`
	Type    string   `json:"type"`
	Shared  bool     `json:"shared"`
	Nodes   []string `json:"nodes"`
	Content []string `json:"content"`
	Free    int64    `json:"free"`
	Total   int64    `json:"total"`
}

// ListVDCs lists the vDCs of the token's tenant.
func (c *Client) ListVDCs(ctx context.Context) ([]VDC, error) {
	var out struct {
		Data []VDC `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/vdcs", nil, nil, &out)
	return out.Data, err
}

// VNets lists the VNets of a vDC.
func (c *Client) VNets(ctx context.Context, vdcID string) ([]VNet, error) {
	var out struct {
		Data []VNet `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/vdcs/%s/vnets", url.PathEscape(vdcID)), nil, nil, &out)
	return out.Data, err
}

// Storages lists the storages of a connection.
func (c *Client) Storages(ctx context.Context, conn string) ([]Storage, error) {
	var out struct {
		Data []Storage `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/connections/%s/storage", url.PathEscape(conn)), nil, nil, &out)
	return out.Data, err
}

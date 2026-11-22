package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// vmListTTL is how long the shared /api/v1/vms list of a connection is reused.
const vmListTTL = 30 * time.Second

// Provisioning is what a clone still sets up after its Proxmox task ended.
type Provisioning struct {
	State    string   `json:"state"`
	Failures []string `json:"failures"`
}

// GuestConfig is the effective Proxmox config of a guest.
type GuestConfig struct {
	Data         map[string]any
	Pending      map[string]any
	MovedTo      string
	Provisioning *Provisioning
}

// CloneRequest is the body of the clone route; other Proxmox keys pass through.
type CloneRequest struct {
	NewID       int    `json:"newid"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Storage     string `json:"storage,omitempty"`
	Target      string `json:"target,omitempty"`
	Full        bool   `json:"full"`
}

// VMSummary is one row of /api/v1/vms.
type VMSummary struct {
	ConnID   string   `json:"connId"`
	Node     string   `json:"node"`
	VMID     int      `json:"-"`
	RawVMID  flexID   `json:"vmid"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Status   string   `json:"status"`
	Template bool     `json:"template"`
	IPs      []string `json:"ips"`
	MACs     []string `json:"macs"`
}

type upidAnswer struct {
	Data *string `json:"data"`
	Node string  `json:"node"`
}

func guestPath(conn, node string, vmid int) string {
	return fmt.Sprintf("/api/v1/connections/%s/guests/qemu/%s/%d", url.PathEscape(conn), url.PathEscape(node), vmid)
}

// NextID asks for the next free VMID (inside the tenant range for a tenant).
func (c *Client) NextID(ctx context.Context, conn string) (int, error) {
	var out struct {
		Data int `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/connections/%s/cluster/nextid", url.PathEscape(conn)), nil, nil, &out); err != nil {
		return 0, err
	}
	return out.Data, nil
}

// Clone clones guest vmid on node and waits for the Proxmox task.
func (c *Client) Clone(ctx context.Context, conn, node string, vmid int, req CloneRequest) error {
	return c.writeAndWait(ctx, http.MethodPost, guestPath(conn, node, vmid)+"/clone", nil, req, conn, node)
}

// GetConfig reads the config of a guest; a 404 is IsNotFound.
func (c *Client) GetConfig(ctx context.Context, conn, node string, vmid int) (GuestConfig, error) {
	var out struct {
		Data         map[string]any `json:"data"`
		MovedTo      *string        `json:"movedTo"`
		Provisioning *Provisioning  `json:"provisioning"`
	}
	if err := c.do(ctx, http.MethodGet, guestPath(conn, node, vmid)+"/config", nil, nil, &out); err != nil {
		return GuestConfig{}, err
	}
	cfg := GuestConfig{Data: out.Data, Provisioning: out.Provisioning, Pending: map[string]any{}}
	if cfg.Data == nil {
		cfg.Data = map[string]any{}
	}
	if p, ok := cfg.Data["pending"].(map[string]any); ok {
		cfg.Pending = p
	}
	delete(cfg.Data, "pending")
	if out.MovedTo != nil {
		cfg.MovedTo = *out.MovedTo
	}
	return cfg, nil
}

// UpdateConfig writes config keys and, when the API answers 202 pending,
// waits for the Proxmox task on the node it names.
func (c *Client) UpdateConfig(ctx context.Context, conn, node string, vmid int, keys map[string]any) error {
	var out struct {
		Pending bool   `json:"pending"`
		UPID    string `json:"upid"`
		Node    string `json:"node"`
	}
	err := c.do(ctx, http.MethodPut, guestPath(conn, node, vmid)+"/config", nil, keys, &out)
	c.invalidate(conn)
	if err != nil {
		return err
	}
	if out.Pending && out.UPID != "" {
		taskNode := out.Node
		if taskNode == "" {
			taskNode = node
		}
		return c.WaitTask(ctx, conn, taskNode, out.UPID)
	}
	return nil
}

// ResizeDisk sets a disk to size (absolute, e.g. "80G") and waits.
func (c *Client) ResizeDisk(ctx context.Context, conn, node string, vmid int, disk, size string) error {
	return c.writeAndWait(ctx, http.MethodPost, guestPath(conn, node, vmid)+"/disk/resize", nil,
		map[string]any{"disk": disk, "size": size}, conn, node)
}

// MoveDisk moves a disk to storage, deleting the source, and waits.
func (c *Client) MoveDisk(ctx context.Context, conn, node string, vmid int, disk, storage string) error {
	return c.writeAndWait(ctx, http.MethodPost, guestPath(conn, node, vmid)+"/disk/move", nil,
		map[string]any{"disk": disk, "storage": storage, "deleteSource": true}, conn, node)
}

// Power runs start, stop, shutdown or reboot and waits.
func (c *Client) Power(ctx context.Context, conn, node string, vmid int, action string) error {
	return c.writeAndWait(ctx, http.MethodPost, guestPath(conn, node, vmid)+"/"+url.PathEscape(action), nil, nil, conn, node)
}

// DeleteVM destroys a stopped guest with its unreferenced disks and waits.
func (c *Client) DeleteVM(ctx context.Context, conn, node string, vmid int) error {
	q := url.Values{"purge": {"1"}, "destroy-unreferenced-disks": {"1"}}
	return c.writeAndWait(ctx, http.MethodDelete, guestPath(conn, node, vmid), q, nil, conn, node)
}

func (c *Client) writeAndWait(ctx context.Context, method, path string, q url.Values, body any, conn, node string) error {
	var out upidAnswer
	err := c.do(ctx, method, path, q, body, &out)
	c.invalidate(conn)
	if err != nil {
		return err
	}
	if out.Data == nil || *out.Data == "" {
		return nil
	}
	taskNode := out.Node
	if taskNode == "" {
		taskNode = node
	}
	return c.WaitTask(ctx, conn, taskNode, *out.Data)
}

// ListVMs reads /api/v1/vms for one connection, optionally only some VMIDs.
func (c *Client) ListVMs(ctx context.Context, conn string, vmids []int) ([]VMSummary, error) {
	q := url.Values{"connId": {conn}}
	if len(vmids) > 0 {
		ids := make([]string, len(vmids))
		for i, id := range vmids {
			ids[i] = strconv.Itoa(id)
		}
		q.Set("vmids", strings.Join(ids, ","))
	}
	var out struct {
		Data struct {
			VMs []VMSummary `json:"vms"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/vms", q, nil, &out); err != nil {
		return nil, err
	}
	vms := make([]VMSummary, 0, len(out.Data.VMs))
	for _, vm := range out.Data.VMs {
		if vm.ConnID != "" && vm.ConnID != conn {
			continue
		}
		n, err := strconv.Atoi(string(vm.RawVMID))
		if err != nil {
			continue
		}
		vm.VMID = n
		vms = append(vms, vm)
	}
	return vms, nil
}

type vmListEntry struct {
	at   time.Time
	vms  map[int]VMSummary
	err  error
	done chan struct{}
}

type vmListCache struct {
	mu      sync.Mutex
	entries map[string]*vmListEntry
}

// CachedVM finds a VM in the shared list of its connection, read at most once
// per vmListTTL whatever the number of resources asking. nil when absent: the
// list may be partial, so absence never means deleted.
func (c *Client) CachedVM(ctx context.Context, conn string, vmid int) (*VMSummary, error) {
	c.vmCache.mu.Lock()
	if c.vmCache.entries == nil {
		c.vmCache.entries = map[string]*vmListEntry{}
	}
	e := c.vmCache.entries[conn]
	if e != nil {
		select {
		case <-e.done:
			if e.err != nil || time.Since(e.at) > vmListTTL {
				e = nil
			}
		default:
		}
	}
	if e == nil {
		e = &vmListEntry{done: make(chan struct{})}
		c.vmCache.entries[conn] = e
		c.vmCache.mu.Unlock()
		vms, err := c.ListVMs(ctx, conn, nil)
		e.at, e.err = time.Now(), err
		e.vms = make(map[int]VMSummary, len(vms))
		for _, vm := range vms {
			e.vms[vm.VMID] = vm
		}
		close(e.done)
	} else {
		c.vmCache.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if e.err != nil {
		return nil, e.err
	}
	vm, ok := e.vms[vmid]
	if !ok {
		return nil, nil
	}
	return &vm, nil
}

func (c *Client) invalidate(conn string) {
	c.vmCache.mu.Lock()
	defer c.vmCache.mu.Unlock()
	delete(c.vmCache.entries, conn)
}

// flexID accepts a JSON string or number.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	*f = flexID(strings.Trim(string(b), `"`))
	return nil
}

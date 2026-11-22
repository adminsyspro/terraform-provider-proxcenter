package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Task is the outcome part of GET /api/v1/tasks/{connectionId}/{node}/{upid}.
type Task struct {
	UPID          string `json:"upid"`
	Node          string `json:"node"`
	Status        string `json:"status"`
	ExitStatus    string `json:"exitstatus"`
	FailureReason string `json:"failureReason"`
}

const (
	taskFirstWait = time.Second
	taskMaxWait   = 5 * time.Second
)

// WaitTask polls a Proxmox task until it stops; nil when it ended OK.
func (c *Client) WaitTask(ctx context.Context, conn, node, upid string) error {
	path := fmt.Sprintf("/api/v1/tasks/%s/%s/%s", url.PathEscape(conn), url.PathEscape(node), url.PathEscape(upid))
	query := url.Values{"log": {"0"}}
	wait := taskFirstWait
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for task %s: %w", upid, err)
		}
		var task Task
		if err := c.do(ctx, http.MethodGet, path, query, nil, &task); err != nil {
			return fmt.Errorf("reading task %s: %w", upid, err)
		}
		if task.Status == "stopped" {
			if task.ExitStatus == "OK" {
				return nil
			}
			reason := task.FailureReason
			if reason == "" {
				reason = task.ExitStatus
			}
			return fmt.Errorf("proxmox task %s failed: %s", upid, reason)
		}
		if err := c.sleep(ctx, time.Duration(float64(wait)*c.jitter())); err != nil {
			return fmt.Errorf("waiting for task %s: %w", upid, err)
		}
		wait = min(time.Duration(float64(wait)*1.5), taskMaxWait)
	}
}

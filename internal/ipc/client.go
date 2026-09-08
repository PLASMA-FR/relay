// Package ipc implements the private Unix-socket client shared by CLI and TUI.
package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/PLASMA-FR/relay/internal/model"
)

type Client struct {
	http   *http.Client
	Socket string
}

func New(socket string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second}
	return &Client{http: &http.Client{Transport: tr}, Socket: socket}
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://relay"+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Relay daemon unavailable at %s; run 'relay service start' or 'relay daemon': %w", c.Socket, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var data struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&data)
		if data.Error == "" {
			data.Error = resp.Status
		}
		return nil, errors.New(data.Error)
	}
	return resp, nil
}
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := c.request(ctx, method, path, in)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(out)
}
func (c *Client) Status(ctx context.Context) (model.Snapshot, error) {
	var s model.Snapshot
	e := c.call(ctx, "GET", "/v1/status", nil, &s)
	return s, e
}
func (c *Client) Send(ctx context.Context, r model.SendRequest) (model.Result, error) {
	var s model.Result
	e := c.call(ctx, "POST", "/v1/send", r, &s)
	return s, e
}
func (c *Client) Action(ctx context.Context, a model.Action) (model.Result, error) {
	var s model.Result
	e := c.call(ctx, "POST", "/v1/action", a, &s)
	return s, e
}
func (c *Client) Watch(ctx context.Context, fn func(model.Snapshot)) error {
	r, e := c.request(ctx, "GET", "/v1/events", nil)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var s model.Snapshot
		if e := json.Unmarshal(scanner.Bytes(), &s); e != nil {
			return fmt.Errorf("decode daemon event: %w", e)
		}
		fn(s)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e := scanner.Err(); e != nil {
		return e
	}
	return io.EOF
}

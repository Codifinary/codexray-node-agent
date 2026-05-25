// Package dockerclient is a minimal HTTP client for the Docker Engine API.
//
// It exists to avoid pulling in the full github.com/docker/docker module, which
// is flagged by SCA scanners for several CVEs that affect dockerd (server-side)
// and the docker CLI — not the client SDK the agent actually uses. As of 2026-05
// three of those CVEs have no upstream fix at all (CVE-2026-41567, -42306,
// -41568), so the only way to clear them from the SBOM is to remove the module
// entirely.
//
// Only the three endpoints the agent needs are implemented:
//   - GET /_ping             (connectivity check + API version detection)
//   - GET /version           (used during API version negotiation)
//   - GET /containers/{id}/json   (the only inspection call the agent makes)
//
// Wire format follows the Docker Engine API as published at
// https://docs.docker.com/engine/api/ and matches what dockerd actually returns.
package dockerclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const defaultAPIVersion = "v1.41"

type Client struct {
	http       *http.Client
	apiVersion string
	host       string
}

// NewClient opens a Docker client against a unix socket path.
func NewClient(socketPath string) (*Client, error) {
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
			Timeout: 30 * time.Second,
		},
		apiVersion: defaultAPIVersion,
		host:       "http://docker",
	}, nil
}

// Ping checks that the daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.host+"/_ping", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker ping: status %d", resp.StatusCode)
	}
	return nil
}

// NegotiateAPIVersion asks the daemon for its API version and pins the client to it.
// Silently falls back to the default if the daemon doesn't return one.
func (c *Client) NegotiateAPIVersion(ctx context.Context) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.host+"/version", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var v struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v.APIVersion == "" {
		return
	}
	c.apiVersion = "v" + v.APIVersion
}

// ContainerInspect returns the subset of the /containers/{id}/json response that
// the agent reads.
func (c *Client) ContainerInspect(ctx context.Context, id string) (*ContainerJSON, error) {
	url := fmt.Sprintf("%s/%s/containers/%s/json", c.host, c.apiVersion, id)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker inspect %s: status %d", id, resp.StatusCode)
	}
	var out ContainerJSON
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ContainerJSON mirrors the fields of github.com/docker/docker/api/types.ContainerJSON
// that the agent reads. Anything not needed is intentionally omitted.
type ContainerJSON struct {
	Name            string
	LogPath         string
	Config          *Config
	HostConfig      *HostConfig
	Mounts          []MountPoint
	NetworkSettings *NetworkSettings
}

type Config struct {
	Image  string
	Labels map[string]string
	Env    []string
}

type HostConfig struct {
	LogConfig LogConfig
}

type LogConfig struct {
	Type string
}

type MountPoint struct {
	Source      string
	Destination string
}

type NetworkSettings struct {
	Ports    map[Port][]PortBinding
	Networks map[string]*EndpointSettings
}

// Port is a "<port>/<protocol>" string (e.g., "80/tcp") matching docker's wire format.
type Port string

// Proto returns the protocol component (after the slash).
func (p Port) Proto() string {
	i := strings.LastIndex(string(p), "/")
	if i < 0 {
		return ""
	}
	return string(p)[i+1:]
}

type PortBinding struct {
	HostIP   string
	HostPort string
}

type EndpointSettings struct {
	NetworkID string
}

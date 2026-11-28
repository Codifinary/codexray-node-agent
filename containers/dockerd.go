// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/internal/dockerclient"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/codifinary/logparser"
	"inet.af/netaddr"
)

const dockerdTimeout = 30 * time.Second

var (
	dockerdClient *dockerclient.Client
	// dockerdInspectFn is DockerdInspect; tests replace it.
	dockerdInspectFn = DockerdInspect
)

func DockerdInit() error {
	c, err := dockerclient.NewClient(proc.HostPath("/run/docker.sock"))
	if err != nil {
		return err
	}
	ctx, cancelFn := context.WithTimeout(context.Background(), dockerdTimeout)
	defer cancelFn()
	if err := c.Ping(ctx); err != nil {
		return err
	}
	c.NegotiateAPIVersion(ctx)
	dockerdClient = c
	return nil
}

func DockerdInspect(containerID string) (*ContainerMetadata, error) {
	if dockerdClient == nil {
		return nil, fmt.Errorf("dockerd client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerdTimeout)
	defer cancel()
	c, err := dockerdClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}
	res := &ContainerMetadata{
		name:        strings.TrimPrefix(c.Name, "/"),
		runtimeID:   c.ID,
		imageID:     c.Image,
		volumes:     map[string]string{},
		hostListens: map[string][]netaddr.IPPort{},
		networks:    map[string]ContainerNetwork{},
		env:         map[string]string{},
	}
	if c.Config != nil {
		res.labels = c.Config.Labels
		res.image = c.Config.Image
		res.env = dockerEnv(c.Config.Env)
	}
	res.otelServiceName, res.otelServiceVersion = common.OtelServiceFromEnv(res.env)
	for _, m := range c.Mounts {
		res.volumes[m.Destination] = common.ParseKubernetesVolumeSource(m.Source)
	}
	if c.LogPath != "" && c.HostConfig != nil && c.HostConfig.LogConfig.Type == "json-file" {
		res.logPath = c.LogPath
		res.logDecoder = logparser.DockerJsonDecoder{}
	}
	if c.NetworkSettings != nil {
		addrs, invalid := dockerPublishedListens(c.NetworkSettings.Ports)
		agentMetrics.dockerPortBindings.WithLabelValues(bindingParseFailed).Add(float64(invalid))
		if len(addrs) > 0 {
			s := make([]netaddr.IPPort, 0, len(addrs))
			for _, addr := range addrs {
				if common.PortFilter.ShouldBeSkipped(addr.Port()) {
					agentMetrics.dockerPortBindings.WithLabelValues(bindingPortFiltered).Inc()
					continue
				}
				agentMetrics.dockerPortBindings.WithLabelValues(bindingOK).Inc()
				s = append(s, addr)
			}
			res.hostListens["dockerd"] = s
		}
		for name, network := range c.NetworkSettings.Networks {
			res.networks[name] = ContainerNetwork{
				NetworkID: network.NetworkID,
			}
		}
	}
	return res, nil
}

// dockerPublishedListens are the host addresses of the TCP ports a container publishes, and how
// many bindings could not be parsed. An empty HostIP binds every interface, of both families.
func dockerPublishedListens(ports map[dockerclient.Port][]dockerclient.PortBinding) ([]netaddr.IPPort, int) {
	seen := map[netaddr.IPPort]bool{}
	invalid := 0
	for port, bindings := range ports {
		if port.Proto() != "tcp" {
			continue
		}
		for _, b := range bindings {
			if b.HostPort == "" {
				continue
			}
			hostPort, err := strconv.ParseUint(b.HostPort, 10, 16)
			if err != nil {
				invalid++
				continue
			}
			if hostPort == 0 {
				continue
			}
			var ips []netaddr.IP
			if host := strings.TrimSuffix(strings.TrimPrefix(b.HostIP, "["), "]"); host == "" {
				ips = []netaddr.IP{netaddr.IPv4(0, 0, 0, 0), netaddr.IPv6Unspecified()}
			} else if ip, err := netaddr.ParseIP(host); err == nil {
				ips = []netaddr.IP{ip.Unmap()}
			} else {
				invalid++
				continue
			}
			for _, ip := range ips {
				seen[netaddr.IPPortFrom(ip, uint16(hostPort))] = true
			}
		}
	}
	res := make([]netaddr.IPPort, 0, len(seen))
	for addr := range seen {
		res = append(res, addr)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].IP() != res[j].IP() {
			return res[i].IP().Less(res[j].IP())
		}
		return res[i].Port() < res[j].Port()
	})
	return res, invalid
}

// dockerEnvKeys are the env keys the agent reads: the Nomad ids calcId builds a container id
// from, and the OpenTelemetry service identity. The rest of the env is not kept.
var dockerEnvKeys = map[string]bool{
	"NOMAD_ALLOC_ID": true, "NOMAD_GROUP_NAME": true, "NOMAD_JOB_NAME": true, "NOMAD_NAMESPACE": true, "NOMAD_TASK_NAME": true,
	common.OtelServiceNameEnv: true, common.OtelResourceAttributesEnv: true,
}

func dockerEnv(env []string) map[string]string {
	res := map[string]string{}
	for _, value := range env {
		k, v, ok := strings.Cut(value, "=")
		if ok && dockerEnvKeys[k] {
			res[k] = v
		}
	}
	return res
}

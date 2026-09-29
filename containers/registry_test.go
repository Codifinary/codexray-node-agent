// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/ebpftracer"
	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/codifinary/codexray-node-agent/gpu"
	"github.com/codifinary/codexray-node-agent/logs"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/mdlayher/taskstats"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netns"
	"golang.org/x/net/dns/dnsmessage"
	"inet.af/netaddr"
)

// registryTestContainerID is a syntactically valid 64-hex runtime container id.
const registryTestContainerID = "3f4c0e1b2a5d6c7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7"

// registryTestNonexistentPid is above any possible pid_max (2^22), so
// /proc/<pid>/cgroup never exists regardless of the host.
const registryTestNonexistentPid = uint32(4294967000)

func registryTestK8sLabels(ns, pod, container string) map[string]string {
	return map[string]string{
		"io.kubernetes.pod.namespace":  ns,
		"io.kubernetes.pod.name":       pod,
		"io.kubernetes.container.name": container,
	}
}

// registryTestCronjobPod builds a CronJob pod name: <cronjob>-<scheduled unix minutes>-<5 safe chars>.
func registryTestCronjobPod(cronjob string, scheduledAt time.Time) string {
	return fmt.Sprintf("%s-%08d-x7k2q", cronjob, scheduledAt.Unix()/60)
}

func TestCalcId(t *testing.T) {
	nomadEnv := map[string]string{
		"NOMAD_ALLOC_ID":   "5b3e7c1a-0000-1111-2222-333344445555",
		"NOMAD_GROUP_NAME": "web",
		"NOMAD_JOB_NAME":   "shop",
		"NOMAD_NAMESPACE":  "default",
		"NOMAD_TASK_NAME":  "frontend",
		"SECRET_TOKEN":     "must-not-matter",
	}
	cases := []struct {
		name string
		cg   *cgroup.Cgroup
		md   *ContainerMetadata
		want ContainerID
	}{
		// kubernetes
		{
			name: "k8s containerd",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeContainerd, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{labels: registryTestK8sLabels("prod", "api-7d9f8c6b5-x2x4z", "api")},
			want: "/k8s/prod/api-7d9f8c6b5-x2x4z/api",
		},
		{
			name: "k8s docker (dockershim)",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{name: "k8s_api_api-0_prod_uid_0", labels: registryTestK8sLabels("prod", "api-0", "api")},
			want: "/k8s/prod/api-0/api",
		},
		{
			name: "k8s crio",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeCrio, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{labels: registryTestK8sLabels("kube-system", "coredns-abc12-zzzzz", "coredns")},
			want: "/k8s/kube-system/coredns-abc12-zzzzz/coredns",
		},
		{
			name: "k8s gVisor sandbox is named 'sandbox' regardless of container label",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeSandbox, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{labels: registryTestK8sLabels("ns1", "pod1", "POD")},
			want: "/k8s/ns1/pod1/sandbox",
		},
		{
			name: "k8s pause container POD is skipped",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeContainerd, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{labels: registryTestK8sLabels("ns1", "pod1", "POD")},
			want: "",
		},
		{
			name: "k8s container without container name is skipped",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeContainerd, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{labels: registryTestK8sLabels("ns1", "pod1", "")},
			want: "",
		},
		{
			name: "runtime container type without a container id is ignored",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeSandbox, ContainerId: ""},
			md:   &ContainerMetadata{labels: registryTestK8sLabels("ns1", "pod1", "app")},
			want: "",
		},

		// docker swarm
		{
			name: "swarm with stack namespace",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md: &ContainerMetadata{name: "shop_web.2.q1w2e3r4", labels: map[string]string{
				"com.docker.swarm.task.name":    "shop_web.2.q1w2e3r4t5y6",
				"com.docker.stack.namespace":    "shop",
				"com.docker.swarm.service.name": "shop_web",
			}},
			want: "/swarm/shop/web/2",
		},
		{
			name: "swarm without stack namespace",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md: &ContainerMetadata{name: "web.1.abc", labels: map[string]string{
				"com.docker.swarm.task.name":    "web.1.abcdef",
				"com.docker.swarm.service.name": "web",
			}},
			want: "/swarm/_/web/1",
		},
		{
			name: "malformed swarm task name falls back to docker name",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md: &ContainerMetadata{name: "web", labels: map[string]string{
				"com.docker.swarm.task.name": "web-no-dots",
			}},
			want: "/docker/web",
		},

		// nomad
		{
			name: "nomad from NOMAD_* env",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{name: "frontend-5b3e7c1a", env: nomadEnv},
			want: "/nomad/default/shop/web/5b3e7c1a-0000-1111-2222-333344445555/frontend",
		},
		{
			name: "incomplete nomad env falls back to docker name",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md: &ContainerMetadata{name: "frontend", env: map[string]string{
				"NOMAD_ALLOC_ID": "x", "NOMAD_JOB_NAME": "shop", "NOMAD_NAMESPACE": "default", "NOMAD_TASK_NAME": "frontend",
			}},
			want: "/docker/frontend",
		},
		{
			name: "non-NOMAD env does not influence the id",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md: &ContainerMetadata{name: "redis", env: map[string]string{
				"PATH": "/usr/bin", "HOSTNAME": "abc", "SECRET_TOKEN": "s3cr3t",
			}},
			want: "/docker/redis",
		},

		// plain docker
		{
			name: "plain docker",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{name: "my-redis"},
			want: "/docker/my-redis",
		},
		{
			name: "docker without name is ignored",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID},
			md:   &ContainerMetadata{},
			want: "",
		},

		// systemd / talos
		{
			name: "systemd service",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: "/system.slice/nginx.service"},
			md:   &ContainerMetadata{},
			want: "/system.slice/nginx.service",
		},
		{
			name: "systemd runtime.slice",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: "/runtime.slice/kubelet.service"},
			md:   &ContainerMetadata{},
			want: "/runtime.slice/kubelet.service",
		},
		{
			name: "crio-conmon systemd scope is skipped",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: "/system.slice/crio-conmon-" + registryTestContainerID + ".scope"},
			md:   &ContainerMetadata{},
			want: "",
		},
		{
			name: "talos runtime",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeTalosRuntime, ContainerId: "/talos/kubelet"},
			md:   &ContainerMetadata{},
			want: "/talos/kubelet",
		},
		{
			name: "talos init",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeTalosRuntime, ContainerId: "/talos/init"},
			md:   &ContainerMetadata{},
			want: "/talos/init",
		},

		// not containers
		{
			name: "standalone process",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeStandaloneProcess},
			md:   &ContainerMetadata{},
			want: "",
		},
		{
			name: "unknown",
			cg:   &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeUnknown, ContainerId: "whatever"},
			md:   &ContainerMetadata{name: "x"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, calcId(tc.cg, tc.md))
		})
	}
}

func TestCalcIdK8sCronjob(t *testing.T) {
	now := time.Now()
	cg := &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeContainerd, ContainerId: registryTestContainerID}
	cases := []struct {
		name string
		pod  string
		want ContainerID
	}{
		{"scheduled now", registryTestCronjobPod("backup", now), "/k8s-cronjob/jobs/backup/worker"},
		{"scheduled 6 days ago", registryTestCronjobPod("nightly-report", now.Add(-6*24*time.Hour)), "/k8s-cronjob/jobs/nightly-report/worker"},
		{"scheduled 6 days ahead (clock skew)", registryTestCronjobPod("backup", now.Add(6*24*time.Hour)), "/k8s-cronjob/jobs/backup/worker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md := &ContainerMetadata{labels: registryTestK8sLabels("jobs", tc.pod, "worker")}
			assert.Equal(t, tc.want, calcId(cg, md))
		})
	}

	// Outside the ±7 day window the 8 digits are not a plausible schedule
	// timestamp, so the pod is treated as an ordinary pod.
	for _, d := range []time.Duration{-8 * 24 * time.Hour, 8 * 24 * time.Hour, -365 * 24 * time.Hour} {
		pod := registryTestCronjobPod("backup", now.Add(d))
		md := &ContainerMetadata{labels: registryTestK8sLabels("jobs", pod, "worker")}
		assert.Equal(t, ContainerID("/k8s/jobs/"+pod+"/worker"), calcId(cg, md), "offset %s", d)
	}

	// Suffix must be 5 chars from the k8s safe alphabet (no vowels, no 0/1/3).
	pod := fmt.Sprintf("backup-%08d-aeiou", now.Unix()/60)
	md := &ContainerMetadata{labels: registryTestK8sLabels("jobs", pod, "worker")}
	assert.Equal(t, ContainerID("/k8s/jobs/"+pod+"/worker"), calcId(cg, md))

	// A deployment pod (replicaset hash, not 8 digits) is not a cronjob.
	md = &ContainerMetadata{labels: registryTestK8sLabels("jobs", "api-7d9f8c6b5-x2x4z", "worker")}
	assert.Equal(t, ContainerID("/k8s/jobs/api-7d9f8c6b5-x2x4z/worker"), calcId(cg, md))

	// The gVisor sandbox of a cronjob pod collapses the same way.
	sb := &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeSandbox, ContainerId: registryTestContainerID}
	md = &ContainerMetadata{labels: registryTestK8sLabels("jobs", registryTestCronjobPod("backup", now), "POD")}
	assert.Equal(t, ContainerID("/k8s-cronjob/jobs/backup/sandbox"), calcId(sb, md))
}

func TestCalcIdIsBoundedAcrossCronjobRuns(t *testing.T) {
	// Every run of a CronJob must map to the same container id so that the
	// container_id label does not grow unbounded in cardinality.
	cg := &cgroup.Cgroup{ContainerType: cgroup.ContainerTypeContainerd, ContainerId: registryTestContainerID}
	ids := map[ContainerID]struct{}{}
	start := time.Now().Add(-3 * 24 * time.Hour)
	for i := 0; i < 100; i++ {
		pod := registryTestCronjobPod("every-hour", start.Add(time.Duration(i)*time.Hour))
		ids[calcId(cg, &ContainerMetadata{labels: registryTestK8sLabels("ops", pod, "job")})] = struct{}{}
	}
	assert.Len(t, ids, 1)
	assert.Contains(t, ids, ContainerID("/k8s-cronjob/ops/every-hour/job"))
}

func TestContainerFilterDefaultAllowsEverything(t *testing.T) {
	// With no --container-allowlist/--container-denylist every id passes.
	// (common.containerFilter can't be constructed with custom lists from this
	// package; the allow/deny semantics are covered in common/container_test.go.)
	require.NotNil(t, common.ContainerFilter)
	for _, id := range []string{"/k8s/default/api-0/api", "/docker/redis", "/system.slice/nginx.service"} {
		assert.False(t, common.ContainerFilter.ShouldBeSkipped(id), id)
	}
}

func TestGetContainerMetadataDispatch(t *testing.T) {
	registryTestResetClients(t)

	t.Run("systemd service asks systemd for TriggeredBy", func(t *testing.T) {
		saved := dbusConn
		dbusConn = nil
		t.Cleanup(func() { dbusConn = saved })
		md, err := getContainerMetadata(&cgroup.Cgroup{ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: "/system.slice/cron.service"})
		require.NoError(t, err)
		require.NotNil(t, md)
		assert.Equal(t, "", md.systemdTriggeredBy)
	})

	for _, ct := range []cgroup.ContainerType{cgroup.ContainerTypeStandaloneProcess, cgroup.ContainerTypeUnknown, cgroup.ContainerTypeLxc, cgroup.ContainerTypeTalosRuntime} {
		t.Run("no runtime lookup for "+ct.String(), func(t *testing.T) {
			md, err := getContainerMetadata(&cgroup.Cgroup{ContainerType: ct, ContainerId: "x"})
			require.NoError(t, err)
			assert.Equal(t, &ContainerMetadata{}, md)
		})
	}

	t.Run("runtime type without container id", func(t *testing.T) {
		md, err := getContainerMetadata(&cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker})
		require.NoError(t, err)
		assert.Equal(t, &ContainerMetadata{}, md)
	})

	t.Run("no runtime clients available", func(t *testing.T) {
		md, err := getContainerMetadata(&cgroup.Cgroup{ContainerType: cgroup.ContainerTypeContainerd, ContainerId: registryTestContainerID})
		assert.Error(t, err)
		assert.Nil(t, md)
	})

	t.Run("crio type goes to cri-o", func(t *testing.T) {
		crioTestServe(t, map[string]string{registryTestContainerID: `{"name":"app","image":"nginx:1","labels":{"io.kubernetes.pod.name":"p"}}`})
		md, err := getContainerMetadata(&cgroup.Cgroup{ContainerType: cgroup.ContainerTypeCrio, ContainerId: registryTestContainerID})
		require.NoError(t, err)
		assert.Equal(t, "nginx:1", md.image)
		assert.Equal(t, "p", md.labels["io.kubernetes.pod.name"])
	})

	t.Run("docker type prefers dockerd", func(t *testing.T) {
		dockerdTestServe(t, map[string]string{registryTestContainerID: `{"Name":"/redis","Config":{"Image":"redis:7"},"HostConfig":{"LogConfig":{"Type":"json-file"}}}`})
		md, err := getContainerMetadata(&cgroup.Cgroup{ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID})
		require.NoError(t, err)
		assert.Equal(t, "redis", md.name)
		assert.Equal(t, "redis:7", md.image)
	})

	t.Run("falls back to containerd when dockerd doesn't know the container", func(t *testing.T) {
		dockerdTestServe(t, map[string]string{})
		containerdTestUse(t, containerdTestContainer{id: registryTestContainerID, image: "docker.io/library/busybox:1"})
		md, err := getContainerMetadata(&cgroup.Cgroup{ContainerType: cgroup.ContainerTypeContainerd, ContainerId: registryTestContainerID})
		require.NoError(t, err)
		assert.Equal(t, "docker.io/library/busybox:1", md.image)
	})

	t.Run("both runtimes fail -> error mentions both", func(t *testing.T) {
		dockerdTestServe(t, map[string]string{})
		containerdTestUse(t)
		md, err := getContainerMetadata(&cgroup.Cgroup{ContainerType: cgroup.ContainerTypeSandbox, ContainerId: registryTestContainerID})
		require.Error(t, err)
		assert.Nil(t, md)
		assert.Contains(t, err.Error(), "dockerd")
		assert.Contains(t, err.Error(), "containerd")
	})
}

// registryTestResetClients nils every runtime client for the duration of the test.
func registryTestResetClients(t *testing.T) {
	d, c, cr := dockerdClient, containerdClient, crioClient
	dockerdClient, containerdClient, crioClient = nil, nil, nil
	t.Cleanup(func() { dockerdClient, containerdClient, crioClient = d, c, cr })
}

func registryTestNewRegistry() *Registry {
	return &Registry{
		reg:                       prometheus.NewRegistry(),
		events:                    make(chan ebpftracer.Event),
		containersById:            map[ContainerID]*Container{},
		containersByCgroupId:      map[string]*Container{},
		containersByPid:           map[uint32]*Container{},
		containersByPidIgnored:    map[uint32]*time.Time{},
		ip2fqdn:                   map[netaddr.IP]*common.Domain{},
		trafficStatsUpdateCh:      make(chan *TrafficStatsUpdate),
		nodejsStatsUpdateCh:       make(chan *NodejsStatsUpdate),
		pythonStatsUpdateCh:       make(chan *PythonStatsUpdate),
		gpuProcessUsageSampleChan: make(chan gpu.ProcessUsageSample),
	}
}

func TestRegistryIp2FqdnCollect(t *testing.T) {
	r := registryTestNewRegistry()
	r.ip2fqdn[netaddr.MustParseIP("10.0.0.1")] = &common.Domain{FQDN: "db.internal", SpecifyIP: true}
	r.ip2fqdn[netaddr.MustParseIP("10.0.0.2")] = &common.Domain{FQDN: "cache.internal", SpecifyIP: false}
	r.ip2fqdn[netaddr.MustParseIP("2001:db8::1")] = &common.Domain{FQDN: "v6.internal", SpecifyIP: true}

	pr := prometheus.NewPedanticRegistry()
	require.NoError(t, pr.Register(r))
	mfs, err := pr.Gather()
	require.NoError(t, err)
	require.Len(t, mfs, 1)
	assert.Equal(t, "ip_to_fqdn", mfs[0].GetName())
	got := map[string]string{}
	for _, m := range mfs[0].GetMetric() {
		labels := map[string]string{}
		for _, l := range m.GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		assert.Equal(t, 1.0, m.GetGauge().GetValue())
		got[labels["ip"]] = labels["fqdn"]
	}
	// only domains flagged SpecifyIP are exported
	assert.Equal(t, map[string]string{"10.0.0.1": "db.internal", "2001:db8::1": "v6.internal"}, got)

	assert.Equal(t, "db.internal", r.getDomain(netaddr.MustParseIP("10.0.0.1")).FQDN)
	assert.Equal(t, "cache.internal", r.getDomain(netaddr.MustParseIP("10.0.0.2")).FQDN)
	assert.Nil(t, r.getDomain(netaddr.MustParseIP("10.9.9.9")))
}

func TestRegistryIgnoredPidCache(t *testing.T) {
	pid := registryTestNonexistentPid

	t.Run("fresh entry short-circuits", func(t *testing.T) {
		r := registryTestNewRegistry()
		fresh := time.Now()
		r.containersByPidIgnored[pid] = &fresh
		assert.Nil(t, r.getOrCreateContainer(pid))
		assert.Contains(t, r.containersByPidIgnored, pid, "a fresh ignore entry must be kept")
	})

	t.Run("expired entry is evicted and the pid is re-evaluated", func(t *testing.T) {
		r := registryTestNewRegistry()
		stale := time.Now().Add(-IgnoredContainersCacheTTL - time.Second)
		r.containersByPidIgnored[pid] = &stale
		assert.Nil(t, r.getOrCreateContainer(pid))
		assert.NotContains(t, r.containersByPidIgnored, pid, "expired entry must be evicted")
		assert.Empty(t, r.containersByPid, "a vanished pid must not be cached")
	})

	t.Run("known pid returns the cached container", func(t *testing.T) {
		r := registryTestNewRegistry()
		c := &Container{id: "/docker/x"}
		r.containersByPid[pid] = c
		assert.Same(t, c, r.getOrCreateContainer(pid))
	})

	t.Run("vanished pid", func(t *testing.T) {
		r := registryTestNewRegistry()
		assert.Nil(t, r.getOrCreateContainer(pid))
		assert.Empty(t, r.containersByPidIgnored)
		assert.Empty(t, r.containersByPid)
	})
}

func TestRegistryUpdateStatsThrottled(t *testing.T) {
	// Within MinTrafficStatsUpdateInterval the eBPF maps must not be touched
	// (tracer is nil here: any access would panic).
	r := registryTestNewRegistry()
	last := time.Now()
	r.ebpfStatsLastUpdated = last
	assert.NotPanics(t, r.updateStatsFromEbpfMapsIfNecessary)
	assert.Equal(t, last, r.ebpfStatsLastUpdated)
}

func TestRegistryHandleEventsDispatch(t *testing.T) {
	r := registryTestNewRegistry()
	pid := registryTestNonexistentPid
	ch := make(chan ebpftracer.Event)
	done := make(chan struct{})
	go func() {
		r.handleEvents(ch)
		close(done)
	}()

	events := []ebpftracer.Event{
		{Type: ebpftracer.EventTypeProcessStart, Pid: pid},
		{Type: ebpftracer.EventTypeProcessExit, Pid: pid, Reason: ebpftracer.EventReasonOOMKill},
		{Type: ebpftracer.EventTypeFileOpen, Pid: pid},
		{Type: ebpftracer.EventTypeListenOpen, Pid: pid, SrcAddr: netaddr.MustParseIPPort("0.0.0.0:80")},
		{Type: ebpftracer.EventTypeListenClose, Pid: pid},
		{Type: ebpftracer.EventTypeConnectionOpen, Pid: pid, DstAddr: netaddr.MustParseIPPort("10.0.0.1:5432")},
		{Type: ebpftracer.EventTypeConnectionError, Pid: pid, DstAddr: netaddr.MustParseIPPort("10.0.0.1:5432")},
		{Type: ebpftracer.EventTypeConnectionClose, Pid: pid},
		{Type: ebpftracer.EventTypeTCPRetransmit, SrcAddr: netaddr.MustParseIPPort("10.0.0.2:40000"), DstAddr: netaddr.MustParseIPPort("10.0.0.1:5432")},
		{Type: ebpftracer.EventTypeL7Request, Pid: pid}, // nil L7Request must be tolerated
		{Type: ebpftracer.EventType(200), Pid: pid},     // unknown type must be tolerated
	}
	for _, e := range events {
		ch <- e
	}
	r.trafficStatsUpdateCh <- nil
	r.trafficStatsUpdateCh <- &TrafficStatsUpdate{Pid: pid, BytesSent: 1}
	r.nodejsStatsUpdateCh <- nil
	r.nodejsStatsUpdateCh <- &NodejsStatsUpdate{Pid: pid}
	r.pythonStatsUpdateCh <- nil
	r.pythonStatsUpdateCh <- &PythonStatsUpdate{Pid: pid}
	r.gpuProcessUsageSampleChan <- gpu.ProcessUsageSample{Pid: pid}

	close(ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleEvents did not return after the events channel was closed")
	}
	// Nothing about a vanished pid may be retained.
	assert.Empty(t, r.containersByPid)
	assert.Empty(t, r.containersById)
	assert.Empty(t, r.containersByCgroupId)
}

func TestRegistryHandleEventsForgetsIgnoredPid(t *testing.T) {
	// A pid recorded as "seen, not a container" (nil entry) must be forgotten on
	// process exit and re-evaluated on process start (pid reuse).
	r := registryTestNewRegistry()
	r.containersByPid[registryTestNonexistentPid] = nil
	r.containersByPid[registryTestNonexistentPid+1] = nil
	ch := make(chan ebpftracer.Event)
	done := make(chan struct{})
	go func() {
		r.handleEvents(ch)
		close(done)
	}()
	ch <- ebpftracer.Event{Type: ebpftracer.EventTypeProcessExit, Pid: registryTestNonexistentPid}
	ch <- ebpftracer.Event{Type: ebpftracer.EventTypeProcessStart, Pid: registryTestNonexistentPid + 1}
	close(ch)
	<-done
	assert.Empty(t, r.containersByPid)
}

// ---- event loop, container discovery and eBPF map polling (through the package seams) ----

func registryTestSeam[T any](t *testing.T, p *T, v T) {
	t.Helper()
	saved := *p
	*p = v
	t.Cleanup(func() { *p = saved })
}

// registryTestCgroups fakes /proc/<pid>/cgroup; unknown pids are gone (ENOENT).
type registryTestCgroups struct {
	lock   sync.Mutex
	byPid  map[uint32]*cgroup.Cgroup
	errs   map[uint32]error
	calls  map[uint32]int
	onRead func(pid uint32)
}

func registryTestUseCgroups(t *testing.T) *registryTestCgroups {
	t.Helper()
	f := &registryTestCgroups{byPid: map[uint32]*cgroup.Cgroup{}, errs: map[uint32]error{}, calls: map[uint32]int{}}
	registryTestSeam(t, &readCgroup, func(pid uint32) (*cgroup.Cgroup, error) {
		f.lock.Lock()
		f.calls[pid]++
		cg, err, onRead := f.byPid[pid], f.errs[pid], f.onRead
		f.lock.Unlock()
		if onRead != nil {
			onRead(pid)
		}
		switch {
		case cg != nil:
			return cg, nil
		case err != nil:
			return nil, err
		}
		return nil, fmt.Errorf("open /proc/%d/cgroup: no such file or directory", pid)
	})
	return f
}

func registryTestNewRegistryWithTracer() *Registry {
	r := registryTestNewRegistry()
	r.tracer = containerTestTracer()
	return r
}

// registryTestContainer is a container known to the registry by id, cgroup and pids.
func registryTestContainer(t *testing.T, r *Registry, id ContainerID, cgId string, pids ...uint32) *Container {
	t.Helper()
	c := containerTestNew(t)
	c.id = id
	c.cgroup = &cgroup.Cgroup{Id: cgId, ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: cgId}
	c.registry = r
	r.containersById[id] = c
	r.containersByCgroupId[cgId] = c
	for _, pid := range pids {
		containerTestAddProcess(c, pid, hostNetNsId)
		r.containersByPid[pid] = c
	}
	return c
}

// registryTestStart runs the event loop; the returned stop closes the events channel
// and waits for the loop to exit (every later read of registry state is then race-free).
func registryTestStart(t *testing.T, r *Registry) (chan<- ebpftracer.Event, func()) {
	t.Helper()
	ch := make(chan ebpftracer.Event)
	done := make(chan struct{})
	go func() {
		r.handleEvents(ch)
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(ch)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("handleEvents did not return after the events channel was closed")
			}
		})
	}
	t.Cleanup(stop)
	return ch, stop
}

func TestRegistryHandleEventsRoutesToContainers(t *testing.T) {
	r := registryTestNewRegistryWithTracer()
	pid := containerTestPid
	c := registryTestContainer(t, r, "/system.slice/app.service", "/system.slice/app.service", pid)
	other := registryTestContainer(t, r, "/system.slice/other.service", "/system.slice/other.service")
	p := c.processes[pid]
	p.nodejsPrevStats = &ebpftracer.NodejsStats{EventLoopBlockedTime: time.Second}
	p.pythonPrevStats = &ebpftracer.PythonStats{ThreadLockWaitTime: time.Second}

	src, dst := containerTestAddr("10.0.0.5:40000"), containerTestAddr("10.0.0.9:5432")
	dns := containerTestDNS(t, dnsmessage.TypeA, "db.example.com.", containerTestDNSAnswer{a: [4]byte{10, 0, 0, 9}})

	ch, stop := registryTestStart(t, r)
	for _, e := range []ebpftracer.Event{
		{Type: ebpftracer.EventTypeListenOpen, Pid: pid, SrcAddr: containerTestAddr("10.0.0.5:8080")},
		{Type: ebpftracer.EventTypeListenClose, Pid: pid, SrcAddr: containerTestAddr("10.0.0.5:8080")},
		{Type: ebpftracer.EventTypeFileOpen, Pid: pid, Fd: 3},
		{Type: ebpftracer.EventTypeConnectionOpen, Pid: pid, Fd: 7, SrcAddr: src, DstAddr: dst, Timestamp: 11, Duration: time.Millisecond},
		{Type: ebpftracer.EventTypeConnectionError, Pid: pid, Fd: 8, SrcAddr: containerTestAddr("10.0.0.5:40001"), DstAddr: containerTestAddr("10.0.0.10:6379")},
		{Type: ebpftracer.EventTypeTCPRetransmit, SrcAddr: src, DstAddr: dst},
		{Type: ebpftracer.EventTypeL7Request, Pid: pid, Fd: 9, L7Request: &l7.RequestData{Protocol: l7.ProtocolDNS, Payload: dns, Duration: time.Millisecond}},
	} {
		ch <- e
	}
	r.trafficStatsUpdateCh <- &TrafficStatsUpdate{Pid: pid, FD: 7, BytesSent: 100, BytesReceived: 200}
	r.nodejsStatsUpdateCh <- &NodejsStatsUpdate{Pid: pid, Stats: ebpftracer.NodejsStats{EventLoopBlockedTime: 3 * time.Second}}
	r.pythonStatsUpdateCh <- &PythonStatsUpdate{Pid: pid, Stats: ebpftracer.PythonStats{ThreadLockWaitTime: 4 * time.Second}}
	now := time.Now()
	r.gpuProcessUsageSampleChan <- gpu.ProcessUsageSample{UUID: "GPU-1", Pid: pid, Timestamp: now, GPUPercent: 50}
	r.gpuProcessUsageSampleChan <- gpu.ProcessUsageSample{UUID: "GPU-1", Pid: pid + 100, Timestamp: now} // unknown pid
	ch <- ebpftracer.Event{Type: ebpftracer.EventTypeConnectionClose, Pid: pid, Fd: 7, Timestamp: 11, TrafficStats: &ebpftracer.TrafficStats{BytesSent: 150, BytesReceived: 250}}
	ch <- ebpftracer.Event{Type: ebpftracer.EventTypeProcessExit, Pid: pid, Reason: ebpftracer.EventReasonOOMKill}
	stop()

	assert.False(t, c.listens[containerTestAddr("10.0.0.5:8080")][pid].ClosedAt.IsZero(), "listen close")

	key := common.NewDestinationKey(dst, dst, nil)
	stats := c.connectionStats[key]
	require.NotNil(t, stats, "connection open")
	assert.Equal(t, uint64(1), stats.Count)
	assert.Equal(t, time.Millisecond, stats.TotalTime)
	assert.Equal(t, uint64(1), stats.Retransmissions, "retransmission attributed to the connection's container")
	assert.Equal(t, uint64(150), stats.BytesSent, "traffic from the stats update and the close event")
	assert.Equal(t, uint64(250), stats.BytesReceived)
	assert.False(t, c.connectionsByPidFd[PidFd{Pid: pid, Fd: 7}].Closed.IsZero(), "connection close")
	assert.Equal(t, int64(1), c.failedConnectionAttempts[containerTestHP("10.0.0.10:6379")], "connection error")
	assert.True(t, p.openSslUprobesChecked && p.goTlsUprobesChecked, "TLS uprobes are checked on the first connection")

	assert.Equal(t, 2*time.Second, c.nodejsStats.EventLoopBlockedTime)
	assert.Equal(t, 3*time.Second, c.pythonStats.ThreadLockWaitTime)
	assert.Len(t, p.gpuUsageSamples, 1)

	assert.Equal(t, 1, c.oomKills, "process exit (OOM)")
	assert.NotContains(t, c.processes, pid)
	assert.NotContains(t, r.containersByPid, pid)

	require.Contains(t, r.ip2fqdn, netaddr.MustParseIP("10.0.0.9"), "DNS answers feed the ip->fqdn map")
	assert.Equal(t, "db.example.com", r.ip2fqdn[netaddr.MustParseIP("10.0.0.9")].FQDN)
	assert.Empty(t, other.connectionStats)
}

func TestRegistryHandleEventsProcessStart(t *testing.T) {
	ts := taskstatsTestUse(t)
	cgs := registryTestUseCgroups(t)
	r := registryTestNewRegistryWithTracer()
	infoCh := make(chan ProcessInfo, 10)
	r.processInfoCh = infoCh

	moved, gone, same := containerTestPid, containerTestPid+1, containerTestPid+2
	old := registryTestContainer(t, r, "/system.slice/old.service", "/system.slice/old.service", moved, gone, same)
	next := registryTestContainer(t, r, "/system.slice/new.service", "/system.slice/new.service")
	cgs.byPid[moved] = next.cgroup // pid reused by a process of another container
	cgs.byPid[same] = old.cgroup
	began := time.Now().Add(-time.Minute).Truncate(time.Second)
	ts.setPID(moved, &taskstats.Stats{BeginTime: began})
	ts.setPID(same, &taskstats.Stats{BeginTime: began})

	ch, stop := registryTestStart(t, r)
	ch <- ebpftracer.Event{Type: ebpftracer.EventTypeProcessStart, Pid: moved}
	ch <- ebpftracer.Event{Type: ebpftracer.EventTypeProcessStart, Pid: gone}
	ch <- ebpftracer.Event{Type: ebpftracer.EventTypeProcessStart, Pid: same}
	stop()

	assert.NotContains(t, old.processes, moved, "a missed process exit is detected by the cgroup change")
	assert.NotContains(t, old.processes, gone, "a vanished pid exits")
	assert.Same(t, next, r.containersByPid[moved])
	require.Contains(t, next.processes, moved)
	assert.Equal(t, began, next.processes[moved].StartedAt)
	assert.Same(t, old, r.containersByPid[same], "same cgroup: kept")
	require.Contains(t, old.processes, same)
	assert.NotContains(t, r.containersByPid, gone)

	close(infoCh)
	var infos []ProcessInfo
	for i := range infoCh {
		infos = append(infos, i)
	}
	assert.ElementsMatch(t, []ProcessInfo{
		{Pid: moved, ContainerId: next.id, StartedAt: began},
		{Pid: same, ContainerId: old.id, StartedAt: began},
	}, infos)
	for _, c := range []*Container{old, next} {
		for _, p := range c.processes {
			p.Close()
		}
	}
}

func TestRegistryHandleEventsGc(t *testing.T) {
	cgs := registryTestUseCgroups(t)
	registryTestSeam(t, &newGcTicker, func(time.Duration) *time.Ticker { return time.NewTicker(time.Millisecond) })
	r := registryTestNewRegistryWithTracer()

	gone, moved, alive, ignoredGone, ignoredAlive := containerTestPid, containerTestPid+1, containerTestPid+2, containerTestPid+3, containerTestPid+4
	live := registryTestContainer(t, r, "/system.slice/live.service", "/system.slice/live.service", gone, moved, alive)
	live.lastConnectionAttempts[containerTestHP("10.0.0.1:5432")] = time.Now()
	r.containersByPid[ignoredGone] = nil
	r.containersByPid[ignoredAlive] = nil
	cgs.byPid[moved] = &cgroup.Cgroup{Id: "/system.slice/other.service"}
	cgs.byPid[alive] = live.cgroup
	cgs.byPid[ignoredAlive] = &cgroup.Cgroup{Id: "/user.slice"}

	dead := registryTestContainer(t, r, "/system.slice/dead.service", "/system.slice/dead.service", containerTestPid+5)
	dead.zombieAt = time.Now().Add(-2 * gcInterval)
	r.containersByPid[containerTestPid+6] = dead // a stale pid entry pointing at the dead container
	cgs.byPid[containerTestPid+5] = dead.cgroup
	cgs.byPid[containerTestPid+6] = dead.cgroup
	require.NoError(t, prometheus.WrapRegistererWith(prometheus.Labels{"container_id": string(dead.id), "app_id": dead.appId}, r.reg).Register(dead))
	unregistered := registryTestContainer(t, r, "/system.slice/unregistered.service", "/system.slice/unregistered.service")
	unregistered.zombieAt = time.Now().Add(-2 * gcInterval)

	now := time.Now()
	r.containersByPidIgnored[containerTestPid+7] = &now
	r.ip2fqdn[netaddr.MustParseIP("10.0.0.1")] = &common.Domain{FQDN: "db.internal"}
	r.ip2fqdn[netaddr.MustParseIP("10.0.0.2")] = &common.Domain{FQDN: "stale.internal"}

	ticked := make(chan struct{})
	var once sync.Once
	cgs.onRead = func(pid uint32) {
		if pid == alive {
			once.Do(func() { close(ticked) })
		}
	}
	_, stop := registryTestStart(t, r)
	select {
	case <-ticked:
	case <-time.After(5 * time.Second):
		t.Fatal("no gc tick")
	}
	stop() // the tick being processed completes first

	assert.Equal(t, map[uint32]*Container{alive: live, ignoredAlive: nil}, r.containersByPid)
	assert.NotContains(t, live.processes, gone, "a vanished pid exits")
	assert.NotContains(t, live.processes, moved, "a pid that moved to another cgroup exits")
	assert.Contains(t, live.processes, alive)

	assert.Equal(t, map[ContainerID]*Container{live.id: live}, r.containersById, "dead containers are deleted")
	assert.Equal(t, map[string]*Container{live.cgroup.Id: live}, r.containersByCgroupId)
	for _, c := range []*Container{dead, unregistered} {
		select {
		case <-c.done:
		default:
			t.Errorf("dead container %s was not closed", c.id)
		}
	}
	assert.False(t, prometheus.WrapRegistererWith(prometheus.Labels{"container_id": string(dead.id), "app_id": dead.appId}, r.reg).Unregister(dead), "unregistered")

	assert.Empty(t, r.containersByPidIgnored, "the ignore cache is reset")
	assert.Equal(t, map[netaddr.IP]*common.Domain{netaddr.MustParseIP("10.0.0.1"): {FQDN: "db.internal"}}, r.ip2fqdn, "only IPs still connected to are kept")
}

// registryTestIterator is a fake eBPF map iterator yielding the given entries.
type registryTestIterator struct {
	entries []func(key, value interface{})
	err     error
}

func (it *registryTestIterator) Next(key, value interface{}) bool {
	if len(it.entries) == 0 {
		return false
	}
	it.entries[0](key, value)
	it.entries = it.entries[1:]
	return true
}

func (it *registryTestIterator) Err() error { return it.err }

func TestRegistryUpdateStatsFromEbpfMaps(t *testing.T) {
	r := registryTestNewRegistryWithTracer()
	pid := containerTestPid
	c := registryTestContainer(t, r, "/system.slice/app.service", "/system.slice/app.service", pid)
	p := c.processes[pid]
	p.nodejsPrevStats = &ebpftracer.NodejsStats{EventLoopBlockedTime: time.Second}
	p.pythonPrevStats = &ebpftracer.PythonStats{ThreadLockWaitTime: time.Second}
	dst := containerTestAddr("10.0.0.9:5432")
	c.onConnectionOpen(pid, 7, containerTestAddr("10.0.0.5:40000"), dst, dst, 0, false, 0)

	var tracers []*ebpftracer.Tracer
	registryTestSeam(t, &activeConnectionsIterator, func(tr *ebpftracer.Tracer) ebpfMapIterator {
		tracers = append(tracers, tr)
		return &registryTestIterator{err: errors.New("iteration aborted"), entries: []func(k, v interface{}){
			func(k, v interface{}) {
				*k.(*ebpftracer.ConnectionId) = ebpftracer.ConnectionId{PID: pid, FD: 7}
				*v.(*ebpftracer.Connection) = ebpftracer.Connection{BytesSent: 100, BytesReceived: 200}
			},
			func(k, v interface{}) {
				*k.(*ebpftracer.ConnectionId) = ebpftracer.ConnectionId{PID: pid + 1, FD: 1} // unknown process
				*v.(*ebpftracer.Connection) = ebpftracer.Connection{BytesSent: 1}
			},
		}}
	})
	registryTestSeam(t, &nodejsStatsIterator, func(tr *ebpftracer.Tracer) ebpfMapIterator {
		tracers = append(tracers, tr)
		return &registryTestIterator{err: errors.New("iteration aborted"), entries: []func(k, v interface{}){func(k, v interface{}) {
			*k.(*uint64) = uint64(pid)
			*v.(*ebpftracer.NodejsStats) = ebpftracer.NodejsStats{EventLoopBlockedTime: 3 * time.Second}
		}}}
	})
	registryTestSeam(t, &pythonStatsIterator, func(tr *ebpftracer.Tracer) ebpfMapIterator {
		tracers = append(tracers, tr)
		return &registryTestIterator{err: errors.New("iteration aborted"), entries: []func(k, v interface{}){func(k, v interface{}) {
			*k.(*uint64) = uint64(pid)
			*v.(*ebpftracer.PythonStats) = ebpftracer.PythonStats{ThreadLockWaitTime: 5 * time.Second}
		}}}
	})

	_, stop := registryTestStart(t, r)
	before := time.Now()
	r.updateStatsFromEbpfMapsIfNecessary() // never updated: polls every map
	assert.False(t, r.ebpfStatsLastUpdated.Before(before))
	r.updateStatsFromEbpfMapsIfNecessary() // throttled
	stop()

	assert.Equal(t, []*ebpftracer.Tracer{r.tracer, r.tracer, r.tracer}, tracers, "each map is polled once")
	stats := c.connectionStats[common.NewDestinationKey(dst, dst, nil)]
	require.NotNil(t, stats)
	assert.Equal(t, uint64(100), stats.BytesSent)
	assert.Equal(t, uint64(200), stats.BytesReceived)
	assert.Equal(t, 2*time.Second, c.nodejsStats.EventLoopBlockedTime)
	assert.Equal(t, 4*time.Second, c.pythonStats.ThreadLockWaitTime, "entries read before an iteration error are applied")
}

func TestRegistryGetOrCreateContainer(t *testing.T) {
	cgs := registryTestUseCgroups(t)
	registryTestResetClients(t)
	registryTestSeam(t, &dbusConn, nil)
	registryTestSeam(t, &journaldReader, nil)
	self := uint32(os.Getpid())
	systemd := func(unit string) *cgroup.Cgroup {
		return &cgroup.Cgroup{Id: "/system.slice/" + unit, ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: "/system.slice/" + unit}
	}

	t.Run("cgroup read error", func(t *testing.T) {
		r := registryTestNewRegistryWithTracer()
		cgs.errs[containerTestPid] = errors.New("permission denied")
		assert.Nil(t, r.getOrCreateContainer(containerTestPid))
		assert.Empty(t, r.containersByPidIgnored, "transient errors are not cached")
	})

	t.Run("known cgroup", func(t *testing.T) {
		r := registryTestNewRegistryWithTracer()
		c := registryTestContainer(t, r, "/system.slice/app.service", "/system.slice/app.service")
		cgs.byPid[containerTestPid+1] = c.cgroup
		assert.Same(t, c, r.getOrCreateContainer(containerTestPid+1))
		assert.Same(t, c, r.containersByPid[containerTestPid+1])
	})

	t.Run("no metadata", func(t *testing.T) {
		r := registryTestNewRegistryWithTracer()
		cgs.byPid[containerTestPid+2] = &cgroup.Cgroup{Id: "/docker/" + registryTestContainerID, ContainerType: cgroup.ContainerTypeDocker, ContainerId: registryTestContainerID}
		assert.Nil(t, r.getOrCreateContainer(containerTestPid+2), "neither dockerd nor containerd know the container")
		assert.Empty(t, r.containersByPidIgnored, "retried on the next event")
	})

	t.Run("not a container", func(t *testing.T) {
		r := registryTestNewRegistryWithTracer()
		cgs.byPid[containerTestPid+3] = &cgroup.Cgroup{Id: "/user.slice/user-1000.slice/session-1.scope", ContainerType: cgroup.ContainerTypeStandaloneProcess}
		assert.Nil(t, r.getOrCreateContainer(containerTestPid+3))
		assert.Contains(t, r.containersByPidIgnored, containerTestPid+3, "cached to avoid re-reading the cgroup")

		// processes briefly in /init.scope (e.g. forked by systemd before being moved) are re-evaluated
		initScope := &cgroup.Cgroup{Id: "/init.scope", ContainerType: cgroup.ContainerTypeStandaloneProcess}
		cgs.byPid[containerTestPid+4] = initScope
		assert.Nil(t, r.getOrCreateContainer(containerTestPid+4))
		assert.NotContains(t, r.containersByPidIgnored, containerTestPid+4)
		cgs.byPid[1] = initScope
		assert.Nil(t, r.getOrCreateContainer(1))
		assert.Contains(t, r.containersByPidIgnored, uint32(1), "systemd itself is ignored for good")
	})

	t.Run("gVisor sandbox", func(t *testing.T) {
		r := registryTestNewRegistryWithTracer()
		sandbox := &cgroup.Cgroup{Id: "/kubepods/besteffort/pod0a1b/sandbox", ContainerType: cgroup.ContainerTypeSandbox}
		// a sandbox that is not runsc has no container id: ignored
		cgs.byPid[containerTestPid+5] = sandbox
		assert.Nil(t, r.getOrCreateContainer(containerTestPid+5))

		// runsc passes the container id as its last argument
		pid := registryTestRunscChild(t, registryTestContainerID)
		cgs.byPid[pid] = &cgroup.Cgroup{Id: sandbox.Id, ContainerType: cgroup.ContainerTypeSandbox}
		dockerdTestServe(t, map[string]string{registryTestContainerID: `{"Name":"/k8s_POD","Config":{"Image":"pause","Labels":{"io.kubernetes.pod.namespace":"ns1","io.kubernetes.pod.name":"pod1","io.kubernetes.container.name":"POD"}},"HostConfig":{}}`})
		c := r.getOrCreateContainer(pid)
		require.NotNil(t, c)
		t.Cleanup(c.Close)
		assert.Equal(t, ContainerID("/k8s/ns1/pod1/sandbox"), c.id)
		assert.Equal(t, registryTestContainerID, c.cgroup.ContainerId)
	})

	t.Run("new container", func(t *testing.T) {
		r := registryTestNewRegistryWithTracer()
		cg := systemd("nginx.service")
		cgs.byPid[self] = cg
		c := r.getOrCreateContainer(self)
		require.NotNil(t, c)
		t.Cleanup(c.Close)
		assert.Equal(t, ContainerID("/system.slice/nginx.service"), c.id)
		assert.Same(t, cg, c.cgroup)
		assert.Same(t, c, r.containersByPid[self])
		assert.Same(t, c, r.containersByCgroupId[cg.Id])
		assert.Same(t, c, r.containersById[c.id])
		assert.True(t, prometheus.WrapRegistererWith(prometheus.Labels{"container_id": string(c.id), "app_id": c.appId}, r.reg).Unregister(c), "registered as a collector")
	})

	t.Run("container vanished before it was created", func(t *testing.T) {
		r := registryTestNewRegistryWithTracer()
		cgs.byPid[containerTestPid+6] = systemd("short-lived.service")
		assert.Nil(t, r.getOrCreateContainer(containerTestPid+6), "its netns can't be opened")
		assert.Empty(t, r.containersById)
	})

	t.Run("collector registration failure", func(t *testing.T) {
		r := registryTestNewRegistryWithTracer()
		cgs.byPid[self] = systemd("dup.service")
		appId := common.ContainerIdToOtelServiceName("/system.slice/dup.service")
		if appId == "/system.slice/dup.service" {
			appId = ""
		}
		clash := registryTestDescCollector{metricsTestCollector{collect: func(chan<- prometheus.Metric) {}}}
		require.NoError(t, prometheus.WrapRegistererWith(prometheus.Labels{"container_id": "/system.slice/dup.service", "app_id": appId}, r.reg).Register(clash))
		assert.Nil(t, r.getOrCreateContainer(self))
		assert.Empty(t, r.containersById)
		assert.Empty(t, r.containersByPid)
	})

	t.Run("id conflict", func(t *testing.T) {
		// a new cgroup for an existing container id (e.g. a restarted systemd unit).
		// cgroup.cgRoot is "" in tests (flags are not parsed), so a v1 cpu cgroup's
		// creation time is read from ./cpu/<path>: fake it under a temporary cwd.
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "cpu/system.slice/restarted.service/payload"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cgroup"), []byte("4:cpu,cpuacct:/system.slice/restarted.service/payload\n"), 0o644))
		t.Chdir(dir)
		fresh, err := cgroup.NewFromProcessCgroupFile("cgroup")
		require.NoError(t, err)
		require.Equal(t, "/system.slice/restarted.service", fresh.ContainerId)
		require.False(t, fresh.CreatedAt().IsZero())

		r := registryTestNewRegistryWithTracer()
		journal := journaldTestUse(t)
		c := registryTestContainer(t, r, "/system.slice/restarted.service", "/system.slice/restarted.service") // no cgroup dir: created at zero time
		cgs.byPid[containerTestPid+7] = fresh
		assert.Same(t, c, r.getOrCreateContainer(containerTestPid+7))
		assert.Same(t, fresh, c.cgroup, "the newer cgroup wins")
		assert.Contains(t, journal.subscribers, fresh.Id, "logs are re-read from the new cgroup")
		assert.Same(t, c, r.containersByCgroupId[fresh.Id])
		assert.Same(t, c, r.containersByPid[containerTestPid+7])
		for _, p := range c.logParsers {
			p.Stop()
		}

		// an older cgroup does not replace the current one
		stale := systemd("restarted.service")
		stale.Id = "/system.slice/restarted.service/stale"
		cgs.byPid[containerTestPid+8] = stale
		assert.Same(t, c, r.getOrCreateContainer(containerTestPid+8))
		assert.Same(t, fresh, c.cgroup)
		assert.Same(t, c, r.containersByCgroupId[stale.Id])
	})
}

// registryTestDescCollector describes itself exactly like a Container does.
type registryTestDescCollector struct{ metricsTestCollector }

func (registryTestDescCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- prometheus.NewDesc("container", "", nil, nil)
}

// registryTestRunscChild starts a process whose command line looks like gVisor's
// `runsc-sandbox ... <container id>`.
func registryTestRunscChild(t *testing.T, containerId string) uint32 {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %s", err)
	}
	cmd := &exec.Cmd{Path: sh, Args: []string{"/usr/local/bin/runsc-sandbox", "-c", "sleep 30; exit 0", "--root=/run/runsc", containerId}}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child process: %s", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pid := uint32(cmd.Process.Pid)
	require.Eventually(t, func() bool { return len(proc.GetCmdline(pid)) > 0 }, 2*time.Second, 5*time.Millisecond)
	return pid
}

// registryTestNewRegistryEnv fakes everything NewRegistry needs from the host: the host
// netns (the agent's own), taskstats, the cgroup namespace, container runtimes under a
// temporary host root and the eBPF tracer. started is closed once the event loop runs.
type registryTestNewRegistryEnv struct {
	hostNsErr, taskstatsErr, cgroupErr, tracerErr error

	started    chan struct{}
	tracerRuns []chan<- ebpftracer.Event
	closed     []*ebpftracer.Tracer
}

func registryTestUseNewRegistryEnv(t *testing.T, env *registryTestNewRegistryEnv) {
	t.Helper()
	env.started = make(chan struct{})
	savedSelf, savedHostId := selfNetNs, hostNetNsId
	t.Cleanup(func() {
		if selfNetNs != savedSelf {
			_ = selfNetNs.Close()
		}
		selfNetNs, hostNetNsId = savedSelf, savedHostId
	})
	registryTestResetClients(t)
	registryTestSeam(t, &taskstatsClient, nil)
	registryTestSeam(t, &journaldReader, nil)
	containerdTestShortHostPath(t)
	containerdTestServeHost(t, "/var/snap/microk8s/common/run/containerd.sock")
	t.Cleanup(func() {
		if containerdClient != nil {
			_ = containerdClient.Close()
		}
	})

	registryTestSeam(t, &getHostNetNs, func() (netns.NsHandle, error) {
		if env.hostNsErr != nil {
			return netns.None(), env.hostNsErr
		}
		return netns.Get()
	})
	registryTestSeam(t, &newTaskstatsClient, func() (*taskstats.Client, error) {
		if env.taskstatsErr != nil {
			return nil, env.taskstatsErr
		}
		return &taskstats.Client{}, nil
	})
	registryTestSeam(t, &cgroupInit, func() error { return env.cgroupErr })
	registryTestSeam(t, &newJournaldReader, func(...string) (*logs.JournaldReader, error) {
		return nil, errors.New("systemd journal not found")
	})
	registryTestSeam(t, &newGcTicker, func(d time.Duration) *time.Ticker {
		close(env.started)
		return time.NewTicker(d)
	})
	registryTestSeam(t, &runTracer, func(tr *ebpftracer.Tracer, ch chan<- ebpftracer.Event) error {
		env.tracerRuns = append(env.tracerRuns, ch)
		return env.tracerErr
	})
	registryTestSeam(t, &closeTracer, func(tr *ebpftracer.Tracer) { env.closed = append(env.closed, tr) })
}

func TestNewRegistry(t *testing.T) {
	env := &registryTestNewRegistryEnv{}
	registryTestUseNewRegistryEnv(t, env)
	reg := prometheus.NewRegistry()
	infoCh := make(chan ProcessInfo)

	r, err := NewRegistry(reg, infoCh, nil)
	require.NoError(t, err)
	require.NotNil(t, r)
	select {
	case <-env.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the event loop was not started")
	}

	assert.True(t, selfNetNs.IsOpen())
	assert.Equal(t, selfNetNs.UniqueId(), hostNetNsId, "the fake host netns is the agent's own")
	assert.NotNil(t, taskstatsClient, "taskstats are initialized inside the host netns")
	assert.NotNil(t, containerdClient, "the first reachable containerd socket is used")
	assert.Nil(t, dockerdClient, "dockerd is optional")
	assert.Nil(t, journaldReader, "journald is optional")
	require.Len(t, env.tracerRuns, 1)
	assert.Equal(t, 10000, cap(r.events))
	assert.NotNil(t, r.tracer)
	assert.Equal(t, (chan<- ProcessInfo)(infoCh), r.processInfoCh)
	assert.True(t, reg.Unregister(r), "the registry exports ip_to_fqdn")

	r.Close()
	assert.Equal(t, []*ebpftracer.Tracer{r.tracer}, env.closed)
	_, open := <-r.events
	assert.False(t, open, "the events channel is closed, stopping the event loop")
}

func TestNewRegistryErrors(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]*registryTestNewRegistryEnv{
		"host netns":  {hostNsErr: boom},
		"taskstats":   {taskstatsErr: boom},
		"cgroup init": {cgroupErr: boom},
		"tracer":      {tracerErr: boom},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			registryTestUseNewRegistryEnv(t, env)
			r, err := NewRegistry(prometheus.NewRegistry(), nil, nil)
			assert.ErrorIs(t, err, boom)
			assert.Nil(t, r)
			if env.tracerErr != nil {
				<-env.started
				require.Len(t, env.tracerRuns, 1)
			} else {
				assert.Empty(t, env.tracerRuns, "the tracer is not started")
			}
		})
	}

	t.Run("already registered", func(t *testing.T) {
		env := &registryTestNewRegistryEnv{}
		registryTestUseNewRegistryEnv(t, env)
		reg := prometheus.NewRegistry()
		require.NoError(t, reg.Register(registryTestNewRegistry()))
		r, err := NewRegistry(reg, nil, nil)
		assert.Error(t, err)
		assert.Nil(t, r)
		assert.Empty(t, env.tracerRuns)
	})
}

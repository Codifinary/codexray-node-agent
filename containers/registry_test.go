// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"fmt"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/ebpftracer"
	"github.com/codifinary/codexray-node-agent/gpu"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

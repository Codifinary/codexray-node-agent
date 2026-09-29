package sd

import (
	"testing"

	"github.com/grafana/pyroscope/ebpf/util"
	"github.com/stretchr/testify/require"
)

func TestDebugString(t *testing.T) {
	target := DiscoveryTarget{"foo": "bar"}
	s := target.DebugString()
	require.Contains(t, s, "foo=bar")
	require.True(t, len(s) > 0)
	require.Equal(t, byte('{'), s[0])
	require.Equal(t, byte('}'), s[len(s)-1])
}

func TestTargetAccessors(t *testing.T) {
	target := NewTargetForTesting("cid1", 42, DiscoveryTarget{
		"service_name": "myservice",
		"myflag":       "true",
	})
	require.Equal(t, "myservice", target.ServiceName())

	v, ok := target.Get("service_name")
	require.True(t, ok)
	require.Equal(t, "myservice", v)

	_, ok = target.Get("does_not_exist")
	require.False(t, ok)

	flag, ok := target.GetFlag("myflag")
	require.True(t, ok)
	require.True(t, flag)

	flag, ok = target.GetFlag("does_not_exist")
	require.False(t, ok)
	require.False(t, flag)

	fp, lbls := target.Labels()
	require.NotZero(t, fp)
	require.Equal(t, "myservice", lbls.Get("service_name"))

	// second call should hit the cached fingerprint path
	fp2, _ := target.Labels()
	require.Equal(t, fp, fp2)

	require.Contains(t, target.String(), "myservice")
}

func TestInferServiceNameVariants(t *testing.T) {
	cases := []struct {
		name     string
		target   DiscoveryTarget
		expected string
	}{
		{
			name:     "k8s explicit annotation",
			target:   DiscoveryTarget{"__meta_kubernetes_pod_annotation_pyroscope_io_service_name": "explicit-svc"},
			expected: "explicit-svc",
		},
		{
			name: "k8s namespace+container",
			target: DiscoveryTarget{
				"__meta_kubernetes_namespace":          "ns",
				"__meta_kubernetes_pod_container_name": "cnt",
			},
			expected: "ebpf/ns/cnt",
		},
		{
			name:     "docker container name",
			target:   DiscoveryTarget{"__meta_docker_container_name": "docker-cnt"},
			expected: "docker-cnt",
		},
		{
			name:     "dockerswarm container label service name",
			target:   DiscoveryTarget{"__meta_dockerswarm_container_label_service_name": "swarm-label-svc"},
			expected: "swarm-label-svc",
		},
		{
			name:     "dockerswarm service name",
			target:   DiscoveryTarget{"__meta_dockerswarm_service_name": "swarm-svc"},
			expected: "swarm-svc",
		},
		{
			name:     "unspecified",
			target:   DiscoveryTarget{},
			expected: "unspecified",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, inferServiceName(tc.target))
		})
	}
}

func TestContainerIDFromTargetVariants(t *testing.T) {
	cases := []struct {
		name     string
		target   DiscoveryTarget
		expected containerID
	}{
		{
			name:     "explicit container id label",
			target:   DiscoveryTarget{labelContainerID: "abc123"},
			expected: "abc123",
		},
		{
			name:     "k8s container id",
			target:   DiscoveryTarget{"__meta_kubernetes_pod_container_id": "docker://abc123"},
			expected: "abc123",
		},
		{
			name:     "docker container id",
			target:   DiscoveryTarget{"__meta_docker_container_id": "dockerid123"},
			expected: "dockerid123",
		},
		{
			name:     "dockerswarm task container id",
			target:   DiscoveryTarget{"__meta_dockerswarm_task_container_id": "swarmtaskid"},
			expected: "swarmtaskid",
		},
		{
			name:     "none",
			target:   DiscoveryTarget{},
			expected: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, containerIDFromTarget(tc.target))
		})
	}
}

func TestPidFromTargetInvalid(t *testing.T) {
	require.Equal(t, uint32(0), pidFromTarget(DiscoveryTarget{}))
	require.Equal(t, uint32(0), pidFromTarget(DiscoveryTarget{labelPID: "not-a-number"}))
	require.Equal(t, uint32(123), pidFromTarget(DiscoveryTarget{labelPID: "123"}))
}

func TestRemoveDeadPIDAndUpdate(t *testing.T) {
	fs, err := newMockFS()
	require.NoError(t, err)
	defer fs.rm()

	options := TargetsOptions{
		Targets: []DiscoveryTarget{
			{
				"__process_pid__": "1801264",
				"service_name":    "svc1",
			},
		},
		TargetsOnly:        true,
		ContainerCacheSize: 1024,
	}

	tf, err := NewTargetFinder(fs.root, util.TestLogger(t), options)
	require.NoError(t, err)

	target := tf.FindTarget(1801264)
	require.NotNil(t, target)

	tf.RemoveDeadPID(1801264)
	target = tf.FindTarget(1801264)
	require.Nil(t, target)

	// Update with a new set of targets replaces the old ones.
	tf.Update(TargetsOptions{
		Targets: []DiscoveryTarget{
			{
				"__process_pid__": "42",
				"service_name":    "svc2",
			},
		},
		TargetsOnly:        true,
		ContainerCacheSize: 2048,
	})

	target = tf.FindTarget(42)
	require.NotNil(t, target)
	require.Equal(t, "svc2", target.ServiceName())
}

func TestDebugInfoAndTargets(t *testing.T) {
	fs, err := newMockFS()
	require.NoError(t, err)
	defer fs.rm()

	options := TargetsOptions{
		Targets: []DiscoveryTarget{
			{
				labelContainerID: "container-abc",
				"service_name":   "svc1",
			},
		},
		TargetsOnly:        true,
		ContainerCacheSize: 1024,
	}

	tf, err := NewTargetFinder(fs.root, util.TestLogger(t), options)
	require.NoError(t, err)

	tfImpl := tf.(*targetFinder)
	targets := tfImpl.Targets()
	require.Len(t, targets, 1)
	require.Equal(t, "svc1", targets[0].ServiceName())

	debugInfo := tf.DebugInfo()
	require.Len(t, debugInfo, 1)
	require.Equal(t, "svc1", debugInfo[0]["service_name"])
}

func TestDefaultTargetUsedWhenNotTargetsOnly(t *testing.T) {
	fs, err := newMockFS()
	require.NoError(t, err)
	defer fs.rm()

	options := TargetsOptions{
		Targets:            nil,
		TargetsOnly:        false,
		DefaultTarget:      DiscoveryTarget{"service_name": "default-svc"},
		ContainerCacheSize: 1024,
	}

	tf, err := NewTargetFinder(fs.root, util.TestLogger(t), options)
	require.NoError(t, err)

	target := tf.FindTarget(999)
	require.NotNil(t, target)
	require.Equal(t, "default-svc", target.ServiceName())
}

// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package cgroup

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFromProcessCgroupFile(t *testing.T) {
	cg, err := NewFromProcessCgroupFile(path.Join("fixtures/proc/100/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/system.slice/docker.service", cg.Id)
	assert.Equal(t, "/system.slice/docker.service", cg.ContainerId)
	assert.Equal(t, ContainerTypeSystemdService, cg.ContainerType)

	assert.Equal(t,
		map[string]string{
			"blkio":        "/system.slice/docker.service",
			"cpu":          "/system.slice/docker.service",
			"cpuacct":      "/system.slice/docker.service",
			"devices":      "/system.slice/docker.service",
			"memory":       "/system.slice/docker.service",
			"name=systemd": "/system.slice/docker.service",
			"pids":         "/system.slice/docker.service",
		},
		cg.subsystems,
	)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/200/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/docker/b43d92bf1e5c6f78bb9b7bc6f40721280299855ba692092716e3a1b6c0b86f3f", cg.Id)
	assert.Equal(t, "b43d92bf1e5c6f78bb9b7bc6f40721280299855ba692092716e3a1b6c0b86f3f", cg.ContainerId)
	assert.Equal(t, ContainerTypeDocker, cg.ContainerType)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/300/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/kubepods/burstable/pod6a4ce4a0-ba47-11ea-b2a7-0cc47ac5979e/17db96a24ae1e9dd57143e62b1cb0d2d35e693c65c774c7470e87b0572e07c1a", cg.Id)
	assert.Equal(t, "17db96a24ae1e9dd57143e62b1cb0d2d35e693c65c774c7470e87b0572e07c1a", cg.ContainerId)
	assert.Equal(t, ContainerTypeDocker, cg.ContainerType)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/400/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod8712f785_1a3e_41ec_a00b_e2dcc77431cb.slice/docker-73051af271105c07e1f493b34856a77e665e3b0b4fc72f76c807dfbffeb881bd.scope", cg.Id)
	assert.Equal(t, "73051af271105c07e1f493b34856a77e665e3b0b4fc72f76c807dfbffeb881bd", cg.ContainerId)
	assert.Equal(t, ContainerTypeDocker, cg.ContainerType)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/600/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/system.slice/springboot.service", cg.Id)
	assert.Equal(t, "/system.slice/springboot.service", cg.ContainerId)
	assert.Equal(t, ContainerTypeSystemdService, cg.ContainerType)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/700/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/podruntime/runtime", cg.Id)
	assert.Equal(t, "/talos/runtime", cg.ContainerId)
	assert.Equal(t, ContainerTypeTalosRuntime, cg.ContainerType)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/800/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/system.slice/docker-cf87ba651579c9231db817909e7865e5747bd7abcac0c57ce23cf4abbaee046b.scope", cg.Id)
	assert.Equal(t, "cf87ba651579c9231db817909e7865e5747bd7abcac0c57ce23cf4abbaee046b", cg.ContainerId)
	assert.Equal(t, ContainerTypeDocker, cg.ContainerType)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/900/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/system.slice/python-app.service", cg.Id)
	assert.Equal(t, "/system.slice/python-app.service", cg.ContainerId)
	assert.Equal(t, ContainerTypeSystemdService, cg.ContainerType)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/2000/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/kubepods/burstable/pod8833712d-6e69-4f5c-95f3-aebd020ce2e7/95cbe853416f52d927dec41f1406dd75015ea131244a1ca875a7cd4ebe927ac8", cg.Id)
	assert.Equal(t, "95cbe853416f52d927dec41f1406dd75015ea131244a1ca875a7cd4ebe927ac8", cg.ContainerId)
	assert.Equal(t, ContainerTypeDocker, cg.ContainerType)

	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/3000/cgroup"))
	require.Nil(t, err)
	assert.Equal(t, "/lxc.payload.first", cg.Id)
	assert.Equal(t, "/lxc/first", cg.ContainerId)
	assert.Equal(t, ContainerTypeLxc, cg.ContainerType)

	baseCgroupPath = "/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-podc83d0428_58af_41eb_8dba_b9e6eddffe7b.slice/docker-0e612005fd07e7f47e2cd07df99a2b4e909446814d71d0b5e4efc7159dd51252.scope"
	defer func() {
		baseCgroupPath = ""
	}()
	cg, err = NewFromProcessCgroupFile(path.Join("fixtures/proc/500/cgroup"))
	assert.Nil(t, err)
	assert.Equal(t, "/system.slice/docker-ba7b10d15d16e10e3de7a2dcd408a3d971169ae303f46cfad4c5453c6326fee2.scope", cg.Id)
	assert.Equal(t, "ba7b10d15d16e10e3de7a2dcd408a3d971169ae303f46cfad4c5453c6326fee2", cg.ContainerId)
	assert.Equal(t, ContainerTypeDocker, cg.ContainerType)
}

func TestContainerByCgroup(t *testing.T) {
	as := assert.New(t)

	typ, id, err := containerByCgroup("/kubepods/burstable/pod9729a196c4723b60ab401eaff722982d/d166c6190614efc91956b78e96d74c3fbc96ca8e91948c36de3bc5b0e7b27d48")
	as.Equal(typ, ContainerTypeDocker)
	as.Equal("d166c6190614efc91956b78e96d74c3fbc96ca8e91948c36de3bc5b0e7b27d48", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/kubepods/besteffort/pod0d08203e-255a-11e9-8cd9-0007cb0b2cc8/671a50f5d60566556912f61511d0ec9e4d5c78d53fbc4676727180438bbbbc55/kube-proxy")
	as.Equal(typ, ContainerTypeDocker)
	as.Equal("671a50f5d60566556912f61511d0ec9e4d5c78d53fbc4676727180438bbbbc55", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/kubepods/poda38c12e8-255a-11e9-8cd9-0007cb0b2cc8/32c562ed81a2622b37b80cb216859820ba51bd694f60ee4cf10d07a4011266f8")
	as.Equal(typ, ContainerTypeDocker)
	as.Equal("32c562ed81a2622b37b80cb216859820ba51bd694f60ee4cf10d07a4011266f8", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/docker/63425c4a8b4291744a79dd9011fddc7a1f8ffda61f65d72196aa01d00cae2e2d")
	as.Equal(typ, ContainerTypeDocker)
	as.Equal("63425c4a8b4291744a79dd9011fddc7a1f8ffda61f65d72196aa01d00cae2e2d", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/docker/63425c4a8b4291744a79dd9011fddc7a1f8ffda61f65d72196aa01d00cae2e2d")
	as.Equal(typ, ContainerTypeDocker)
	as.Equal("63425c4a8b4291744a79dd9011fddc7a1f8ffda61f65d72196aa01d00cae2e2d", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/lxc/mysql-primary-db")
	as.Equal(typ, ContainerTypeLxc)
	as.Equal("mysql-primary-db", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/kubepods/poda48c12e8-255a-11e9-8cd9-0007cb0b2cc8/crio-63425c4a8b4291744a79dd9011fddc7a1f8ffda61f65d72196aa01d00cae2e2e")
	as.Equal(typ, ContainerTypeCrio)
	as.Equal("63425c4a8b4291744a79dd9011fddc7a1f8ffda61f65d72196aa01d00cae2e2e", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod2942c55e_c9cb_428a_93f4_eaf89c1f3ce0.slice/crio-49f9e8e5395d57c1083996c09e2e6f042d5fe1ec0310facab32f94912b35ce59.scope")
	as.Equal(typ, ContainerTypeCrio)
	as.Equal("49f9e8e5395d57c1083996c09e2e6f042d5fe1ec0310facab32f94912b35ce59", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-podea19ff5d_943a_4466_a07e_a71e9e50cc62.slice/crio-21572039dd8398ff8272b031fa5422a40165145ab37f2f8794e1e7f844fe8118.scope/container")
	as.Equal(typ, ContainerTypeCrio)
	as.Equal("21572039dd8398ff8272b031fa5422a40165145ab37f2f8794e1e7f844fe8118", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod3e61c214bc3ed9ff81e21474dd6cba17.slice/cri-containerd-c74b0f5062f0bc726cae1e9369ad4a95deed6b298d247f0407475adb23fa3190")
	as.Equal(typ, ContainerTypeContainerd)
	as.Equal("c74b0f5062f0bc726cae1e9369ad4a95deed6b298d247f0407475adb23fa3190", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/system.slice/system-serial\\x2dgetty.slice")
	as.Equal(typ, ContainerTypeSystemdService)
	as.Equal("/system.slice/system-serial-getty.slice", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/runtime.slice/kubelet.service")
	as.Equal(typ, ContainerTypeSystemdService)
	as.Equal("/runtime.slice/kubelet.service", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/reserved.slice/kubelet.service")
	as.Equal(typ, ContainerTypeSystemdService)
	as.Equal("/reserved.slice/kubelet.service", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/system.slice/system-postgresql.slice/postgresql@9.4-main.service")
	as.Equal(typ, ContainerTypeSystemdService)
	as.Equal("/system.slice/system-postgresql.slice", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/system.slice/containerd.service/kubepods-burstable-pod4ed02c0b_0df8_4d14_a30e_fd589ee4143a.slice:cri-containerd:d4a9f9195eaf7e4a729f24151101e1de61f1398677e7b82acfb936dff0b4ce55")
	as.Equal(typ, ContainerTypeContainerd)
	as.Equal("d4a9f9195eaf7e4a729f24151101e1de61f1398677e7b82acfb936dff0b4ce55", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/podruntime/kubelet")
	as.Equal(typ, ContainerTypeTalosRuntime)
	as.Equal("/talos/kubelet", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/system/dashboard")
	as.Equal(typ, ContainerTypeTalosRuntime)
	as.Equal("/talos/dashboard", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/init")
	as.Equal(typ, ContainerTypeTalosRuntime)
	as.Equal("/talos/init", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/lxc.payload.first")
	as.Equal(typ, ContainerTypeLxc)
	as.Equal("/lxc/first", id)
	as.Nil(err)

	typ, id, err = containerByCgroup("/lxc.monitor.first")
	as.Equal(ContainerTypeStandaloneProcess, typ)
	as.Equal("", id)
	as.Nil(err)
}

func cgroupTestWriteFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func cgroupTestSetRoots(t *testing.T, v1, v2 string) {
	t.Helper()
	prevV1, prevV2, prevBase := cgRoot, cg2Root, baseCgroupPath
	cgRoot, cg2Root, baseCgroupPath = v1, v2, ""
	t.Cleanup(func() { cgRoot, cg2Root, baseCgroupPath = prevV1, prevV2, prevBase })
}

func TestContainerByCgroupRuntimes(t *testing.T) {
	const id64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		name    string
		path    string
		typ     ContainerType
		id      string
		wantErr bool
	}{
		{"docker v1", "/docker/" + id64, ContainerTypeDocker, id64, false},
		{"docker systemd scope", "/system.slice/docker-" + id64 + ".scope", ContainerTypeDocker, id64, false},
		{"docker invalid id", "/docker/short", ContainerTypeUnknown, "", true},
		{"docker uppercase id is not a docker id", "/docker/" + strings.ToUpper(id64), ContainerTypeUnknown, "", true},
		{"crio kubepods cgroupfs", "/kubepods/besteffort/pod1/crio-" + id64, ContainerTypeCrio, id64, false},
		{"crio conmon ignored", "/kubepods.slice/kubepods-pod1.slice/crio-conmon-" + id64 + ".scope", ContainerTypeUnknown, "", false},
		{"containerd dash", "/kubepods.slice/kubepods-pod1.slice/cri-containerd-" + id64 + ".scope", ContainerTypeContainerd, id64, false},
		{"containerd colon", "/system.slice/containerd.service/kubepods-pod1.slice:cri-containerd:" + id64, ContainerTypeContainerd, id64, false},
		{"kubepods sandbox pod level", "/kubepods/burstable/pod6a4ce4a0-ba47-11ea-b2a7-0cc47ac5979e", ContainerTypeSandbox, "", false},
		{"kubepods root", "/kubepods.slice", ContainerTypeSandbox, "", false},
		{"kubepods docker", "/kubepods/pod1/" + id64, ContainerTypeDocker, id64, false},
		{"lxc", "/lxc/web01", ContainerTypeLxc, "web01", false},
		{"lxc nested", "/lxc/web01/init.scope", ContainerTypeLxc, "web01", false},
		{"lxc without name", "/lxc", ContainerTypeUnknown, "", true},
		{"lxc payload", "/lxc.payload.web01", ContainerTypeLxc, "/lxc/web01", false},
		{"lxc payload nested", "/lxc.payload.web01/system.slice/x.service", ContainerTypeLxc, "/lxc/web01", false},
		{"lxc monitor", "/lxc.monitor.web01", ContainerTypeStandaloneProcess, "", false},
		{"system slice service", "/system.slice/nginx.service", ContainerTypeSystemdService, "/system.slice/nginx.service", false},
		{"system slice escaped", "/system.slice/system-foo\\x2dbar.slice/a.service", ContainerTypeSystemdService, "/system.slice/system-foo-bar.slice", false},
		{"runtime slice", "/runtime.slice/containerd.service", ContainerTypeSystemdService, "/runtime.slice/containerd.service", false},
		{"reserved slice", "/reserved.slice/kubelet.service", ContainerTypeSystemdService, "/reserved.slice/kubelet.service", false},
		{"bare system.slice", "/system.slice", ContainerTypeUnknown, "", true},
		{"talos system", "/system/apid", ContainerTypeTalosRuntime, "/talos/apid", false},
		{"talos podruntime", "/podruntime/etcd", ContainerTypeTalosRuntime, "/talos/etcd", false},
		{"talos init", "/init", ContainerTypeTalosRuntime, "/talos/init", false},
		{"talos bare system", "/system", ContainerTypeUnknown, "", true},
		{"user slice", "/user.slice/user-1000.slice/session-1.scope", ContainerTypeStandaloneProcess, "", false},
		{"init scope", "/init.scope", ContainerTypeStandaloneProcess, "", false},
		{"root", "/", ContainerTypeStandaloneProcess, "", false},
		{"empty", "", ContainerTypeStandaloneProcess, "", false},
		{"single unknown level", "/something", ContainerTypeStandaloneProcess, "", false},
		{"unknown nested", "/foo/bar", ContainerTypeUnknown, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typ, id, err := containerByCgroup(c.path)
			if c.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, c.typ, typ)
			assert.Equal(t, c.id, id)
		})
	}
}

func TestNewFromProcessCgroupFileEdgeCases(t *testing.T) {
	cgroupTestSetRoots(t, cgRoot, cg2Root)
	dir := t.TempDir()
	const id64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("process exited", func(t *testing.T) {
		_, err := NewFromProcessCgroupFile(filepath.Join(dir, "missing", "cgroup"))
		require.Error(t, err)
		assert.True(t, common.IsNotExist(err))
	})

	t.Run("empty and malformed lines", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "malformed/cgroup", "\ngarbage\n1:cpu\n0::/\n")
		cg, err := NewFromProcessCgroupFile(p)
		require.NoError(t, err)
		assert.Equal(t, "", cg.Id)
		assert.Equal(t, ContainerTypeStandaloneProcess, cg.ContainerType)
		assert.Empty(t, cg.subsystems)
	})

	t.Run("only name=systemd set", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "systemd/cgroup", "5:cpu,cpuacct:/\n1:name=systemd:/system.slice/cron.service\n")
		cg, err := NewFromProcessCgroupFile(p)
		require.NoError(t, err)
		assert.Equal(t, "/system.slice/cron.service", cg.Id)
		assert.Equal(t, ContainerTypeSystemdService, cg.ContainerType)
	})

	t.Run("hybrid prefers v1 kubepods over v2 path", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "hybrid/cgroup",
			"6:memory:/kubepods/besteffort/pod1/"+id64+"\n"+
				"4:cpu,cpuacct:/\n"+
				"0::/system.slice/containerd.service\n")
		cg, err := NewFromProcessCgroupFile(p)
		require.NoError(t, err)
		assert.Equal(t, "/kubepods/besteffort/pod1/"+id64, cg.Id)
		assert.Equal(t, ContainerTypeDocker, cg.ContainerType)
		assert.Equal(t, id64, cg.ContainerId)
	})

	t.Run("v1 memory used when cpu missing", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "memonly/cgroup", "6:memory:/docker/"+id64+"\n")
		cg, err := NewFromProcessCgroupFile(p)
		require.NoError(t, err)
		assert.Equal(t, "/docker/"+id64, cg.Id)
		assert.Equal(t, ContainerTypeDocker, cg.ContainerType)
	})

	t.Run("init.scope is ignored", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "initscope/cgroup", "0::/init.scope\n")
		cg, err := NewFromProcessCgroupFile(p)
		require.NoError(t, err)
		assert.Equal(t, "", cg.Id)
		assert.Equal(t, ContainerTypeStandaloneProcess, cg.ContainerType)
	})

	t.Run("lxc payload without sub path", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "lxc/cgroup", "0::/lxc.payload.c1\n")
		cg, err := NewFromProcessCgroupFile(p)
		require.NoError(t, err)
		assert.Equal(t, "/lxc.payload.c1", cg.Id)
		assert.Equal(t, "/lxc/c1", cg.ContainerId)
		assert.Equal(t, ContainerTypeLxc, cg.ContainerType)
	})

	t.Run("lxc payload v1 nested controllers collapse", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "lxc1/cgroup",
			"4:cpu,cpuacct:/lxc.payload.c2/init.scope\n6:memory:/lxc.payload.c2/system.slice/a.service\n")
		cg, err := NewFromProcessCgroupFile(p)
		require.NoError(t, err)
		assert.Equal(t, "/lxc.payload.c2", cg.subsystems["cpu"])
		assert.Equal(t, "/lxc.payload.c2", cg.subsystems["memory"])
		assert.Equal(t, "/lxc/c2", cg.ContainerId)
	})

	t.Run("unknown runtime returns error", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "unknown/cgroup", "0::/foo/bar\n")
		_, err := NewFromProcessCgroupFile(p)
		assert.Error(t, err)
	})

	t.Run("talos init via file", func(t *testing.T) {
		p := cgroupTestWriteFile(t, dir, "talos/cgroup", "0::/init\n")
		cg, err := NewFromProcessCgroupFile(p)
		require.NoError(t, err)
		assert.Equal(t, ContainerTypeTalosRuntime, cg.ContainerType)
		assert.Equal(t, "/talos/init", cg.ContainerId)
	})
}

func TestCgroupCreatedAt(t *testing.T) {
	root := t.TempDir()
	cgroupTestSetRoots(t, root, filepath.Join(root, "unified"))

	mtime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(p, 0o755))
		require.NoError(t, os.Chtimes(p, mtime, mtime))
	}
	mk("unified/system.slice/a.service")
	mk("cpu/system.slice/b.service")
	mk("memory/system.slice/c.service")

	cg := &Cgroup{subsystems: map[string]string{"": "/system.slice/a.service"}}
	assert.True(t, mtime.Equal(cg.CreatedAt()))

	cg = &Cgroup{subsystems: map[string]string{"cpu": "/system.slice/b.service"}}
	assert.True(t, mtime.Equal(cg.CreatedAt()))

	cg = &Cgroup{subsystems: map[string]string{"memory": "/system.slice/c.service"}}
	assert.True(t, mtime.Equal(cg.CreatedAt()))

	// cgroup already removed (container exited) -> zero time, no panic
	cg = &Cgroup{subsystems: map[string]string{"": "/system.slice/gone.service"}}
	assert.True(t, cg.CreatedAt().IsZero())

	// no usable controller
	cg = &Cgroup{subsystems: map[string]string{"pids": "/x"}}
	assert.True(t, cg.CreatedAt().IsZero())
}

func TestContainerTypeString(t *testing.T) {
	assert.Equal(t, "standalone", ContainerTypeStandaloneProcess.String())
	assert.Equal(t, "docker", ContainerTypeDocker.String())
	assert.Equal(t, "crio", ContainerTypeCrio.String())
	assert.Equal(t, "cri-containerd", ContainerTypeContainerd.String())
	assert.Equal(t, "lxc", ContainerTypeLxc.String())
	assert.Equal(t, "systemd", ContainerTypeSystemdService.String())
	assert.Equal(t, "unknown", ContainerTypeUnknown.String())
}

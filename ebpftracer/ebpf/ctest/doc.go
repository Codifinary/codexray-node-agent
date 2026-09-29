// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

// Package ctest compiles the kernel-side L7 protocol classifiers
// (ebpftracer/ebpf/l7/*.c) with the host C compiler, via host_shim.h, and
// exposes them to Go tests. It is test-only and needs cgo and a C compiler.
package ctest

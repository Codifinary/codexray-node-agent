// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/codifinary/codexray-node-agent/flags"
	"k8s.io/klog/v2"
)

type containerFilter struct {
	allowList []*regexp.Regexp
	denyList  []*regexp.Regexp
}

var ContainerFilter *containerFilter

func init() {
	var err error
	allowList := *flags.ContainerAllowlist
	if !strings.HasSuffix(os.Args[0], ".test") {
		if allowList, err = validateContainerAllowlist(allowList); err != nil {
			klog.Exitf("invalid container allowlist: %s", err)
		}
	}
	if ContainerFilter, err = newContainerFilter(allowList, *flags.ContainerDenylist); err != nil {
		klog.Exitf("invalid container filter: %s", err)
	}
}

// validateContainerAllowlist requires at least one allowlist pattern and rejects
// empty entries and "*". It returns the trimmed patterns.
func validateContainerAllowlist(allowList []string) ([]string, error) {
	if len(allowList) == 0 {
		return nil, errors.New("--container-allowlist (CONTAINER_ALLOWLIST) is required")
	}
	res := make([]string, 0, len(allowList))
	for _, v := range allowList {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, errors.New("empty pattern")
		}
		if strings.Trim(v, "*") == "" {
			return nil, fmt.Errorf("%q is not allowed: list the containers to monitor", v)
		}
		res = append(res, v)
	}
	return res, nil
}

func newContainerFilter(allowList, denyList []string) (*containerFilter, error) {
	f := &containerFilter{}
	klog.Infoln("container allowlist:", allowList)
	klog.Infoln("container denylist:", denyList)
	for _, v := range allowList {
		r, err := regexp.Compile(v)
		if err != nil {
			return nil, err
		}
		f.allowList = append(f.allowList, r)
	}
	for _, v := range denyList {
		r, err := regexp.Compile(v)
		if err != nil {
			return nil, err
		}
		f.denyList = append(f.denyList, r)
	}
	return f, nil
}

func (f *containerFilter) ShouldBeSkipped(containerId string) bool {
	if f.skippedByDenylist(containerId) {
		return true
	}
	return f.skippedByAllowlist(containerId)
}

func (f *containerFilter) skippedByAllowlist(containerId string) bool {
	if len(f.allowList) == 0 {
		return false
	}
	for _, v := range f.allowList {
		if v.MatchString(containerId) {
			return false
		}
	}
	return true
}

func (f *containerFilter) skippedByDenylist(containerId string) bool {
	if len(f.denyList) == 0 {
		return false
	}
	for _, v := range f.denyList {
		if v.MatchString(containerId) {
			return true
		}
	}
	return false
}

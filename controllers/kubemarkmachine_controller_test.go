/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubemark/api/v1alpha4"
)

const (
	kubemarkExtendedResourcesFlag  = "--extended-resources"
	kubemarkNodeLabelsFlag         = "--node-labels"
	kubemarkRegisterWithTaintsFlag = "--register-with-taints"
)

func TestGetKubemarkExtendedResourcesFlag(t *testing.T) {
	tests := []struct {
		name          string
		resources     infrav1.KubemarkExtendedResourceList
		expectedFlags string // the expected flags string does not need to be in a specific order
	}{
		{
			name:          "empty string",
			resources:     nil,
			expectedFlags: "",
		},
		{
			name: "cpu",
			resources: infrav1.KubemarkExtendedResourceList{
				infrav1.KubemarkExtendedResourceCPU: resource.MustParse("2"),
			},
			expectedFlags: fmt.Sprintf("%s=cpu=2", kubemarkExtendedResourcesFlag),
		},
		{
			name: "memory",
			resources: infrav1.KubemarkExtendedResourceList{
				infrav1.KubemarkExtendedResourceMemory: resource.MustParse("16G"),
			},
			expectedFlags: fmt.Sprintf("%s=memory=16G", kubemarkExtendedResourcesFlag),
		},
		{
			name: "cpu, memory, gpu",
			resources: infrav1.KubemarkExtendedResourceList{
				infrav1.KubemarkExtendedResourceCPU:    resource.MustParse("2"),
				infrav1.KubemarkExtendedResourceMemory: resource.MustParse("16G"),
				"nvidia.com/gpu":                       resource.MustParse("1"),
			},
			expectedFlags: fmt.Sprintf("%s=cpu=2,memory=16G,nvidia.com/gpu=1", kubemarkExtendedResourcesFlag),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observedFlags := getKubemarkExtendedResourcesFlag(tt.resources)
			observed, err := mapFromFlags(kubemarkExtendedResourcesFlag, observedFlags)
			if err != nil {
				t.Error("unable to process observed flag string", err)
			}
			expected, err := mapFromFlags(kubemarkExtendedResourcesFlag, tt.expectedFlags)
			if err != nil {
				t.Error("unable to process expected flag string", err)
			}
			if !reflect.DeepEqual(observed, expected) {
				t.Error("observed flags did not match expected", observedFlags, tt.expectedFlags)
			}
		})
	}
}

func TestGetKubemarkRegisterWithTaintsFlag(t *testing.T) {
	tests := []struct {
		name          string
		taints        []corev1.Taint
		expectedFlags string
	}{
		{
			name:          "empty array",
			expectedFlags: "",
		},
		{
			name: "one taint",
			taints: []corev1.Taint{
				{
					Key:    "some.taint/key",
					Effect: "NoExecute",
					Value:  "some-value",
				},
			},
			expectedFlags: fmt.Sprintf("%s=some.taint/key=some-value:NoExecute", kubemarkRegisterWithTaintsFlag),
		},
		{
			name: "two taints, one without value",
			taints: []corev1.Taint{
				{
					Key:    "some.taint/key",
					Effect: "NoExecute",
					Value:  "some-value",
				},
				{
					Key:    "some-other.taint/key",
					Effect: "NoSchedule",
				},
			},
			expectedFlags: fmt.Sprintf("%s=some.taint/key=some-value:NoExecute,some-other.taint/key:NoSchedule", kubemarkRegisterWithTaintsFlag),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observedFlags := getKubemarkRegisterWithTaintsFlag(tt.taints)
			if observedFlags != tt.expectedFlags {
				t.Error("observed flags did not match expected", observedFlags, tt.expectedFlags)
			}
		})
	}
}

func TestGetKubemarkNodeLabelsFlag(t *testing.T) {
	tests := []struct {
		name          string
		labels        map[string]string
		expectedFlags string // the expected flags string does not need to be in a specific order
	}{
		{
			name:          "empty map",
			labels:        map[string]string{},
			expectedFlags: "",
		},
		{
			name: "map with one label",
			labels: map[string]string{
				"label.io/one": "1",
			},
			expectedFlags: fmt.Sprintf("%s=label.io/one=1", kubemarkNodeLabelsFlag),
		},
		{
			name: "map with two label",
			labels: map[string]string{
				"label.io/one": "1",
				"label.io/two": "2",
			},
			expectedFlags: fmt.Sprintf("%s=label.io/one=1,label.io/two=2", kubemarkNodeLabelsFlag),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observedFlags := getKubemarkNodeLabelsFlag(tt.labels)
			observed, err := mapFromFlags(kubemarkNodeLabelsFlag, observedFlags)
			if err != nil {
				t.Error("unable to process observed flag string", err)
			}
			expected, err := mapFromFlags(kubemarkNodeLabelsFlag, tt.expectedFlags)
			if err != nil {
				t.Error("unable to process expected flag string", err)
			}
			if !reflect.DeepEqual(observed, expected) {
				t.Error("observed flags did not match expected", observedFlags, tt.expectedFlags)
			}
		})
	}
}

// This is a helper function for processing the extended resources command line flags.
// It accepts a string in the format of the flag and returns a map of resources and quantities.
func mapFromFlags(prefix, flags string) (map[string]string, error) {
	if flags == "" {
		return nil, nil
	}

	if !strings.HasPrefix(flags, prefix) {
		return nil, errors.New(fmt.Sprintf("extended resources flag does not contain proper prefix `%s`, `%s`", prefix, flags))
	}

	ret := map[string]string{}
	// create an array of resources strings (eg "cpu=1")
	// we want to split the flag and equal sign from the string
	resources := strings.Split(flags[len(prefix)+1:], ",")
	for _, r := range resources {
		// split the resource string into its key and value
		rsplit := strings.Split(r, "=")
		if len(rsplit) != 2 {
			return nil, errors.New(fmt.Sprintf("unable to split resource pair `%s` in `%s`", r, flags))
		}
		ret[rsplit[0]] = rsplit[1]
	}

	return ret, nil
}

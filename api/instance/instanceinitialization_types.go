/*
Copyright 2026 The Kubernetes Authors.

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

package instance

// InitializationFormat is a specific supported instance initialization flow.
// +kubebuilder:validation:Enum="";cloudinit;ignition
type InitializationFormat string

const (
	// CloudInitType represents a cloud-init initialization.
	CloudInitType InitializationFormat = "cloudinit"

	// IgnitionType represent an ignition initialization.
	IgnitionType InitializationFormat = "ignition"
)

// +kubebuilder:object:generate:=true

// +kubebuilder:object:root:=true
// InstanceInitializationSpec defines options for how an instance is initialized.
type InstanceInitializationSpec struct {
	// format is the underlying initialization flow that is utilized for an instance.
	// This value controls how an instance will be initialized after it boots but before
	// it joins a kubernetes cluster. Allowed values are "cloudinit" and "ignition".
	//
	// +unionDiscriminator
	Format InitializationFormat `json:"format"`

	// cloudinit contains the settings for the cloud-init initialization flow.
	CloudInit *CloudInitSpec `json:"cloudinit,omitempty"`

	// ignition contains the settings for the ignition initialization flow.
	Ignition *IgnitionSpec `json:"ignition,omitempty"`
}

// CloudInitSpec holds configurations details related to the cloud-init initialization flow.
type CloudInitSpec struct {
}

// IgnitionSpec holds configurations details related to the ignition initialization flow.
type IgnitionSpec struct {
}

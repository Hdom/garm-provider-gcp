// SPDX-License-Identifier: Apache-2.0
// Copyright 2024 Cloudbase Solutions SRL
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package spec

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/cloudbase/garm-provider-common/cloudconfig"
	"github.com/cloudbase/garm-provider-common/defaults"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm-provider-common/util"
	"github.com/cloudbase/garm-provider-gcp/config"
	"github.com/xeipuuv/gojsonschema"
)

const (
	defaultDiskSizeGB     int64  = 127
	defaultNicType        string = "VIRTIO_NET"
	garmPoolID            string = "garmpoolid"
	garmControllerID      string = "garmcontrollerid"
	osType                string = "ostype"
	customLabelKeyRegex   string = "^\\p{Ll}[\\p{Ll}0-9_-]{0,62}$"
	customLabelValueRegex string = "^[\\p{Ll}0-9_-]{0,63}$"
	networkTagRegex       string = "^[a-z][a-z0-9-]{0,61}[a-z0-9]$"
	jsonSchema            string = `
		{
			"$schema": "http://cloudbase.it/garm-provider-gcp/schemas/extra_specs#",
			"type": "object",
			"description": "Schema defining supported extra specs for the Garm GCP Provider",
			"properties": {
				"disksize": {
					"type": "integer",
					"description": "The size of the root disk in GB. Default is 127 GB."
				},
				"network_id": {
					"type": "string",
					"description": "The name of the network attached to the instance."
				},
				"subnet_id": {
					"type": "string",
					"description": "The name of the subnetwork attached to the instance."
				},
				"nic_type": {
					"type": "string",
					"description": "The type of NIC attached to the instance. Default is VIRTIO_NET."
				},
				"custom_labels":{
					"type": "object",
					"description": "Custom labels to be attached to the instance. Each label is a key-value pair where both key and value are strings.",
					"additionalProperties": {
						"type": "string"
					}
				},
				"network_tags": {
					"type": "array",
					"description": "A list of network tags to be attached to the instance.",
					"items": {
						"type": "string"
					}
				},
				"source_snapshot": {
					"type": "string",
					"description": "The source snapshot to create this disk."
				}
			},
			"additionalProperties": false
		}
	`
)

type ToolFetchFunc func(osType params.OSType, osArch params.OSArch, tools []params.RunnerApplicationDownload) (params.RunnerApplicationDownload, error)

var DefaultToolFetch ToolFetchFunc = util.GetTools

func jsonSchemaValidation(schema json.RawMessage) error {
	schemaLoader := gojsonschema.NewStringLoader(jsonSchema)
	extraSpecsLoader := gojsonschema.NewBytesLoader(schema)
	result, err := gojsonschema.Validate(schemaLoader, extraSpecsLoader)
	if err != nil {
		return fmt.Errorf("failed to validate schema: %w", err)
	}
	if !result.Valid() {
		return fmt.Errorf("schema validation failed: %s", result.Errors())
	}
	return nil
}

func newExtraSpecsFromBootstrapData(data params.BootstrapInstance) (*extraSpecs, error) {
	spec := &extraSpecs{}

	if err := jsonSchemaValidation(data.ExtraSpecs); err != nil {
		return nil, fmt.Errorf("failed to validate extra specs: %w", err)
	}

	if len(data.ExtraSpecs) > 0 {
		if err := json.Unmarshal(data.ExtraSpecs, spec); err != nil {
			return nil, fmt.Errorf("failed to unmarshal extra specs: %w", err)
		}
	}

	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("failed to validate extra specs: %w", err)
	}

	return spec, nil
}

func (e *extraSpecs) Validate() error {
	if len(e.CustomLabels) > 61 {
		return fmt.Errorf("custom labels cannot exceed 61 items")
	}
	keyRegex, err := regexp.Compile(customLabelKeyRegex)
	if err != nil {
		return fmt.Errorf("invalid key regex pattern: %w", err)

	}
	valueRegex, err := regexp.Compile(customLabelValueRegex)
	if err != nil {
		return fmt.Errorf("invalid value regex pattern: %w", err)
	}
	for key, value := range e.CustomLabels {
		if !keyRegex.MatchString(key) {
			return fmt.Errorf("custom label key '%s' does not match requirements", key)
		}
		if !valueRegex.MatchString(value) {
			return fmt.Errorf("custom label value '%s' does not match requirements", value)
		}
	}
	if len(e.NetworkTags) > 64 {
		return fmt.Errorf("network tags cannot exceed 64 items")
	}
	tagRegex, err := regexp.Compile(networkTagRegex)
	if err != nil {
		return fmt.Errorf("invalid tag regex pattern: %w", err)
	}
	for _, tag := range e.NetworkTags {
		if !tagRegex.MatchString(tag) {
			return fmt.Errorf("network tag '%s' does not match requirements", tag)
		}
	}
	return nil
}

type extraSpecs struct {
	DiskSize       int64             `json:"disksize,omitempty"`
	NetworkID      string            `json:"network_id,omitempty"`
	SubnetworkID   string            `json:"subnetwork_id,omitempty"`
	NicType        string            `json:"nic_type,omitempty"`
	CustomLabels   map[string]string `json:"custom_labels,omitempty"`
	NetworkTags    []string          `json:"network_tags,omitempty"`
	SourceSnapshot string            `json:"source_snapshot,omitempty"`
}

func GetRunnerSpecFromBootstrapParams(cfg *config.Config, data params.BootstrapInstance, controllerID string) (*RunnerSpec, error) {
	tools, err := DefaultToolFetch(data.OSType, data.OSArch, data.Tools)
	if err != nil {
		return nil, fmt.Errorf("failed to get tools: %s", err)
	}

	extraSpecs, err := newExtraSpecsFromBootstrapData(data)
	if err != nil {
		return nil, fmt.Errorf("error loading extra specs: %w", err)
	}

	labels := map[string]string{
		garmPoolID:       data.PoolID,
		garmControllerID: controllerID,
		osType:           string(data.OSType),
	}

	spec := &RunnerSpec{
		Zone:            cfg.Zone,
		Tools:           tools,
		BootstrapParams: data,
		NetworkID:       cfg.NetworkID,
		SubnetworkID:    cfg.SubnetworkID,
		ControllerID:    controllerID,
		NicType:         defaultNicType,
		DiskSize:        defaultDiskSizeGB,
		CustomLabels:    labels,
	}

	spec.MergeExtraSpecs(extraSpecs)

	return spec, nil
}

type RunnerSpec struct {
	Zone            string
	Tools           params.RunnerApplicationDownload
	BootstrapParams params.BootstrapInstance
	NetworkID       string
	SubnetworkID    string
	ControllerID    string
	NicType         string
	DiskSize        int64
	CustomLabels    map[string]string
	NetworkTags     []string
	SourceSnapshot  string
}

func (r *RunnerSpec) MergeExtraSpecs(extraSpecs *extraSpecs) {
	if extraSpecs.NetworkID != "" {
		r.NetworkID = extraSpecs.NetworkID
	}
	if extraSpecs.SubnetworkID != "" {
		r.SubnetworkID = extraSpecs.SubnetworkID
	}
	if extraSpecs.DiskSize > 0 {
		r.DiskSize = extraSpecs.DiskSize
	}
	if extraSpecs.NicType != "" {
		r.NicType = extraSpecs.NicType
	}
	if len(extraSpecs.CustomLabels) > 0 {
		maps.Copy(r.CustomLabels, extraSpecs.CustomLabels)
	}
	if len(extraSpecs.NetworkTags) > 0 {
		r.NetworkTags = extraSpecs.NetworkTags
	}
	if extraSpecs.SourceSnapshot != "" {
		r.SourceSnapshot = extraSpecs.SourceSnapshot
	}
}

func (r *RunnerSpec) Validate() error {
	if r.Zone == "" {
		return fmt.Errorf("missing zone")
	}
	if r.NetworkID == "" {
		return fmt.Errorf("missing network id")
	}
	if r.SubnetworkID == "" {
		return fmt.Errorf("missing subnetwork id")
	}
	if r.ControllerID == "" {
		return fmt.Errorf("missing controller id")
	}
	if r.NicType == "" {
		return fmt.Errorf("missing nic type")
	}

	return nil
}

func (r RunnerSpec) ComposeUserData() (string, error) {
	switch r.BootstrapParams.OSType {
	case params.Linux:
		udata, err := cloudconfig.GetRunnerInstallScript(r.BootstrapParams, r.Tools, r.BootstrapParams.Name)
		if err != nil {
			return "", fmt.Errorf("failed to generate userdata: %w", err)
		}

		asBase64 := base64.StdEncoding.EncodeToString(udata)
		scriptCommands := []string{
			"sudo useradd -m " + defaults.DefaultUser + " || true",
			// Create the runner home directory if it doesn't exist
			"sudo mkdir -p /home/" + defaults.DefaultUser,
			// Add user to sudoers
			"sudo usermod -aG sudo " + defaults.DefaultUser,
			// Check curl and tar are installed
			"sudo apt-get update && sudo apt-get install -y curl tar",
			// Install the runner
			"echo " + asBase64 + " | base64 -d > /install_runner.sh",
			"chmod +x /install_runner.sh",
			"echo 'runner  ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/garm",
			"su -l -c /install_runner.sh " + defaults.DefaultUser,
		}
		script := strings.Join(scriptCommands, "\n")
		return script, nil
	case params.Windows:
		udata, err := cloudconfig.GetRunnerInstallScript(r.BootstrapParams, r.Tools, r.BootstrapParams.Name)
		if err != nil {
			return "", fmt.Errorf("failed to generate userdata: %w", err)
		}

		return string(udata), nil
	}
	return "", fmt.Errorf("unsupported OS type for cloud config: %s", r.BootstrapParams.OSType)
}

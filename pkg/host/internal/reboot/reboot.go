/*
Copyright 2026.

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
package reboot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

type rebootTracker struct{}

func New() types.RebootTrackerInterface {
	return &rebootTracker{}
}

func (r *rebootTracker) ReadRebootTracker() (*types.RebootTrackerFile, error) {
	path := utils.GetHostExtensionPath(consts.SriovRebootTrackerFilePath)
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Log.V(2).Info("ReadRebootTracker(): file does not exist")
			return nil, nil
		}
		log.Log.Error(err, "ReadRebootTracker(): failed to check reboot tracker file", "path", path)
		return nil, err
	}

	rawData, err := os.ReadFile(path)
	if err != nil {
		log.Log.Error(err, "ReadRebootTracker(): failed to read reboot tracker file", "path", path)
		return nil, err
	}

	tracker := &types.RebootTrackerFile{}
	err = yaml.Unmarshal(rawData, tracker)
	if err != nil {
		log.Log.Error(err, "ReadRebootTracker(): failed to unmarshal reboot tracker file", "path", path)
		return nil, err
	}
	return tracker, nil
}

func (r *rebootTracker) WriteRebootTracker(tracker *types.RebootTrackerFile) error {
	path := utils.GetHostExtensionPath(consts.SriovRebootTrackerFilePath)
	dir := filepath.Dir(path)

	_, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(dir, 0o755)
			if err != nil {
				log.Log.Error(err, "WriteRebootTracker(): failed to create directory", "path", dir)
				return err
			}
		} else {
			log.Log.Error(err, "WriteRebootTracker(): failed to check directory", "path", dir)
			return err
		}
	}

	out, err := yaml.Marshal(tracker)
	if err != nil {
		log.Log.Error(err, "WriteRebootTracker(): failed to marshal reboot tracker")
		return err
	}

	log.Log.V(2).Info("WriteRebootTracker(): write tracker",
		"content", string(out), "path", path)
	err = os.WriteFile(path, out, 0o644)
	if err != nil {
		log.Log.Error(err, "WriteRebootTracker(): failed to write reboot tracker file", "path", path)
		return err
	}

	return nil
}

func (r *rebootTracker) ReadBootID() (string, error) {
	content, err := os.ReadFile(utils.GetHostExtensionPath(consts.ProcKernelBootID))
	if err != nil {
		return "", fmt.Errorf("failed to read boot_id: %v", err)
	}
	return strings.TrimSpace(string(content)), nil
}

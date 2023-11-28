/*


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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

var webhooks = map[string](string){
	constants.InjectorWebHookName: constants.InjectorWebHookPath,
	constants.OperatorWebHookName: constants.OperatorWebHookPath,
}

const (
	clusterRoleResourceName               = "ClusterRole"
	clusterRoleBindingResourceName        = "ClusterRoleBinding"
	mutatingWebhookConfigurationCRDName   = "MutatingWebhookConfiguration"
	validatingWebhookConfigurationCRDName = "ValidatingWebhookConfiguration"
	machineConfigCRDName                  = "MachineConfig"
)

var namespace = os.Getenv("NAMESPACE")

type DrainAnnotationPredicate struct {
	predicate.Funcs
}

func (DrainAnnotationPredicate) Create(e event.CreateEvent) bool {
	logger := log.FromContext(context.TODO())
	if e.Object == nil {
		logger.Info("Create event: node has no drain annotation", "node", e.Object.GetName())
		return false
	}

	if _, hasAnno := e.Object.GetAnnotations()[constants.NodeDrainAnnotation]; hasAnno {
		logger.Info("Create event: node has no drain annotation", "node", e.Object.GetName())
		return true
	}
	return false
}

func (DrainAnnotationPredicate) Update(e event.UpdateEvent) bool {
	logger := log.FromContext(context.TODO())
	if e.ObjectOld == nil {
		logger.Info("Update event has no old object to update", "node", e.ObjectOld.GetName())
		return false
	}
	if e.ObjectNew == nil {
		logger.Info("Update event has no new object for update", "node", e.ObjectNew.GetName())
		return false
	}

	oldAnno, hasOldAnno := e.ObjectOld.GetAnnotations()[constants.NodeDrainAnnotation]
	newAnno, hasNewAnno := e.ObjectNew.GetAnnotations()[constants.NodeDrainAnnotation]

	if !hasOldAnno && hasNewAnno {
		return true
	}

	if oldAnno != newAnno {
		return true
	}

	return false
}

type DrainStateAnnotationPredicate struct {
	predicate.Funcs
}

func (DrainStateAnnotationPredicate) Create(e event.CreateEvent) bool {
	logger := log.FromContext(context.TODO())
	if e.Object == nil {
		logger.Info("Create event: node has no drain annotation", "node", e.Object.GetName())
		return false
	}

	if _, hasAnno := e.Object.GetAnnotations()[constants.NodeStateDrainAnnotationCurrent]; hasAnno {
		logger.Info("Create event: node has no drain annotation", "node", e.Object.GetName())
		return true
	}
	return false
}

func (DrainStateAnnotationPredicate) Update(e event.UpdateEvent) bool {
	logger := log.FromContext(context.TODO())
	if e.ObjectOld == nil {
		logger.Info("Update event has no old object to update", "node", e.ObjectOld.GetName())
		return false
	}
	if e.ObjectNew == nil {
		logger.Info("Update event has no new object for update", "node", e.ObjectNew.GetName())
		return false
	}

	oldAnno, hasOldAnno := e.ObjectOld.GetAnnotations()[constants.NodeStateDrainAnnotationCurrent]
	newAnno, hasNewAnno := e.ObjectNew.GetAnnotations()[constants.NodeStateDrainAnnotationCurrent]

	if !hasOldAnno || !hasNewAnno {
		return true
	}

	if oldAnno != newAnno {
		return true
	}

	return oldAnno != newAnno
}

func GetImagePullSecrets() []string {
	imagePullSecrets := os.Getenv("IMAGE_PULL_SECRETS")
	if imagePullSecrets != "" {
		return strings.Split(imagePullSecrets, ",")
	} else {
		return []string{}
	}
}

func formatJSON(str string) (string, error) {
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, []byte(str), "", "    "); err != nil {
		return "", err
	}
	return prettyJSON.String(), nil
}

func GetDefaultNodeSelector() map[string]string {
	return map[string]string{"node-role.kubernetes.io/worker": "",
		"kubernetes.io/os": "linux"}
}

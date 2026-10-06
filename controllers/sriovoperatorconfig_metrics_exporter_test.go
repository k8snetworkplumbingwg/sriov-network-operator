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

package controllers

import (
	"context"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	orchestratorMock "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/orchestrator/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/render"
)

func TestMetricsExporterSELinuxType(t *testing.T) {
	for _, clusterType := range []consts.ClusterType{consts.ClusterTypeKubernetes, consts.ClusterTypeOpenshift} {
		t.Run(string(clusterType), func(t *testing.T) {
			for _, seLinuxType := range []string{"", "spc_t", "sriov_metrics_t"} {
				t.Run("type="+seLinuxType, func(t *testing.T) {
					g := NewGomegaWithT(t)
					t.Setenv("METRICS_EXPORTER_SELINUX_TYPE", seLinuxType)
					t.Setenv("METRICS_EXPORTER_IMAGE", "metrics-exporter:latest")
					t.Setenv("METRICS_EXPORTER_PORT", "9110")
					t.Setenv("METRICS_EXPORTER_SECRET_NAME", "metrics-exporter-cert")
					t.Setenv("METRICS_EXPORTER_KUBE_RBAC_PROXY_IMAGE", "kube-rbac-proxy:latest")
					t.Setenv("TLS_CIPHER_SUITES", "")
					t.Setenv("TLS_MIN_VERSION", "")
					t.Setenv("TLS_CURVE_PREFERENCES", "")
					t.Setenv("METRICS_EXPORTER_PROMETHEUS_OPERATOR_ENABLED", "false")

					orchestrator := orchestratorMock.NewMockInterface(gomock.NewController(t))
					orchestrator.EXPECT().ClusterType().Return(clusterType).AnyTimes()
					orchestrator.EXPECT().GetTLSConfig(gomock.Any()).Return(nil, nil)

					scheme := runtime.NewScheme()
					g.Expect(sriovnetworkv1.AddToScheme(scheme)).To(Succeed())
					featureGate := featuregate.New()
					featureGate.Init(map[string]bool{consts.MetricsExporterFeatureGate: true})

					var daemonSet *appsv1.DaemonSet
					reconciler := &SriovOperatorConfigReconciler{
						Scheme:       scheme,
						Orchestrator: orchestrator,
						FeatureGate:  featureGate,
						renderManifestFn: func(path string, data *render.RenderData) ([]*unstructured.Unstructured, error) {
							return render.RenderDir(filepath.Join("..", path), data)
						},
						applyManifestFn: func(_ context.Context, _ client.Client, obj *unstructured.Unstructured) error {
							if obj.GetKind() != "DaemonSet" {
								return nil
							}
							daemonSet = &appsv1.DaemonSet{}
							return runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, daemonSet)
						},
					}
					config := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
					g.Expect(reconciler.syncMetricsExporter(context.Background(), config)).To(Succeed())
					g.Expect(daemonSet).NotTo(BeNil())

					for _, container := range daemonSet.Spec.Template.Spec.Containers {
						securityContext := container.SecurityContext
						g.Expect(securityContext).NotTo(BeNil())
						if container.Name == "metrics-exporter" && seLinuxType != "" {
							g.Expect(securityContext.SELinuxOptions).NotTo(BeNil())
							g.Expect(securityContext.SELinuxOptions.Type).To(Equal(seLinuxType))
						} else {
							g.Expect(securityContext.SELinuxOptions).To(BeNil())
						}
						g.Expect(securityContext.ReadOnlyRootFilesystem).To(HaveValue(BeTrue()))
						g.Expect(securityContext.AllowPrivilegeEscalation).To(HaveValue(BeFalse()))
					}
				})
			}
		})
	}
}

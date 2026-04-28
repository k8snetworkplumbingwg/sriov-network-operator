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
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// --- Phase 4: DRA helper unit tests (GetNodeSelectorForDRADriver, syncDRADriverObjs, cleanupDRADriverObjs) ---

var _ = Describe("Helper DRA", func() {
	Context("GetNodeSelectorForDRADriver", func() {
		It("returns ConfigDaemonNodeSelector when set", func() {
			dc := &sriovnetworkv1.SriovOperatorConfig{
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					ConfigDaemonNodeSelector: map[string]string{
						"custom-label": "value",
						"foo":          "bar",
					},
				},
			}
			sel := GetNodeSelectorForDRADriver(dc)
			Expect(sel).To(HaveLen(2))
			Expect(sel).To(HaveKeyWithValue("custom-label", "value"))
			Expect(sel).To(HaveKeyWithValue("foo", "bar"))
		})

		It("returns default node selector when ConfigDaemonNodeSelector is empty", func() {
			dc := &sriovnetworkv1.SriovOperatorConfig{
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					ConfigDaemonNodeSelector: map[string]string{},
				},
			}
			sel := GetNodeSelectorForDRADriver(dc)
			Expect(sel).To(Equal(GetDefaultNodeSelector()))
			Expect(sel).To(HaveKeyWithValue("node-role.kubernetes.io/worker", ""))
			Expect(sel).To(HaveKeyWithValue("kubernetes.io/os", "linux"))
		})

		It("returns default node selector when ConfigDaemonNodeSelector is nil", func() {
			dc := &sriovnetworkv1.SriovOperatorConfig{}
			sel := GetNodeSelectorForDRADriver(dc)
			Expect(sel).To(Equal(GetDefaultNodeSelector()))
		})
	})

	Context("cleanupDRADriverObjs", func() {
		var (
			ctx     context.Context
			scheme  *runtime.Scheme
			client  k8sclient.Client
			nsSaved string
		)

		BeforeEach(func() {
			ctx = context.Background()
			nsSaved = vars.Namespace
			vars.Namespace = testNamespace
			DeferCleanup(func() { vars.Namespace = nsSaved })
			scheme = runtime.NewScheme()
			utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
			utilruntime.Must(corev1.AddToScheme(scheme))
			utilruntime.Must(appsv1.AddToScheme(scheme))
			utilruntime.Must(rbacv1.AddToScheme(scheme))
		})

		It("deletes DRA driver DaemonSet, RBAC, ServiceAccount, and base DeviceClass when present", func() {
			ds := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      draDriverDaemonSetName,
					Namespace: testNamespace,
				},
			}
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: draDriverServiceAccountName, Namespace: testNamespace},
			}
			role := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{Name: draDriverPodAccessRoleName, Namespace: testNamespace},
			}
			roleBinding := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: draDriverPodAccessRoleBindingName, Namespace: testNamespace},
			}
			clusterRole := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: draDriverClusterRBACName},
			}
			clusterRoleBinding := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: draDriverClusterRBACName},
			}
			dc := &unstructured.Unstructured{}
			dc.SetGroupVersionKind(schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceClass"})
			dc.SetName(draDriverBaseDeviceClassName)

			client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				ds, sa, role, roleBinding, clusterRole, clusterRoleBinding, dc,
			).Build()
			Expect(cleanupDRADriverObjs(ctx, client)).To(Succeed())

			Expect(client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: draDriverDaemonSetName}, &appsv1.DaemonSet{})).To(MatchError(ContainSubstring("not found")))
			Expect(client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: draDriverServiceAccountName}, &corev1.ServiceAccount{})).To(MatchError(ContainSubstring("not found")))
			Expect(client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: draDriverPodAccessRoleName}, &rbacv1.Role{})).To(MatchError(ContainSubstring("not found")))
			Expect(client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: draDriverPodAccessRoleBindingName}, &rbacv1.RoleBinding{})).To(MatchError(ContainSubstring("not found")))
			Expect(client.Get(ctx, types.NamespacedName{Name: draDriverClusterRBACName}, &rbacv1.ClusterRole{})).To(MatchError(ContainSubstring("not found")))
			Expect(client.Get(ctx, types.NamespacedName{Name: draDriverClusterRBACName}, &rbacv1.ClusterRoleBinding{})).To(MatchError(ContainSubstring("not found")))
			Expect(client.Get(ctx, types.NamespacedName{Name: draDriverBaseDeviceClassName}, dc)).To(MatchError(ContainSubstring("not found")))
		})

		It("succeeds when DRA driver DaemonSet does not exist", func() {
			client = fake.NewClientBuilder().WithScheme(scheme).Build()
			Expect(cleanupDRADriverObjs(ctx, client)).To(Succeed())
		})
	})

	Context("syncDRADriverObjs", func() {
		var (
			ctx     context.Context
			scheme  *runtime.Scheme
			client  k8sclient.Client
			dc      *sriovnetworkv1.SriovOperatorConfig
			nsSaved string
		)

		BeforeEach(func() {
			ctx = context.Background()
			nsSaved = vars.Namespace
			vars.Namespace = testNamespace
			DeferCleanup(func() { vars.Namespace = nsSaved })
			DeferCleanup(os.Setenv, "SRIOV_DRA_DRIVER_IMAGE", os.Getenv("SRIOV_DRA_DRIVER_IMAGE"))
			DeferCleanup(os.Setenv, "SRIOV_NETWORK_CONFIG_DAEMON_IMAGE", os.Getenv("SRIOV_NETWORK_CONFIG_DAEMON_IMAGE"))
			Expect(os.Setenv("SRIOV_DRA_DRIVER_IMAGE", "dra-driver-image")).To(Succeed())
			Expect(os.Setenv("SRIOV_NETWORK_CONFIG_DAEMON_IMAGE", "config-daemon-image")).To(Succeed())
			scheme = runtime.NewScheme()
			utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
			utilruntime.Must(corev1.AddToScheme(scheme))
			utilruntime.Must(appsv1.AddToScheme(scheme))
			utilruntime.Must(rbacv1.AddToScheme(scheme))
			dc = &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: testNamespace},
			}
			client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(dc).Build()
		})

		It("creates DRA driver DaemonSet and other objects", func() {
			Expect(syncDRADriverObjs(ctx, client, scheme, dc)).To(Succeed())
			ds := &appsv1.DaemonSet{}
			Expect(client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "sriov-dra-driver"}, ds)).To(Succeed())
			Expect(ds.Namespace).To(Equal(testNamespace))
			Expect(ds.Name).To(Equal("sriov-dra-driver"))
		})

		It("uses GetNodeSelectorForDRADriver for DaemonSet node selector", func() {
			dc.Spec.ConfigDaemonNodeSelector = map[string]string{"node-role.kubernetes.io/worker": ""}
			Expect(syncDRADriverObjs(ctx, client, scheme, dc)).To(Succeed())
			ds := &appsv1.DaemonSet{}
			Expect(client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "sriov-dra-driver"}, ds)).To(Succeed())
			Expect(ds.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("node-role.kubernetes.io/worker", ""))
		})
	})
})

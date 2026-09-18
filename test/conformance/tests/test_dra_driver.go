package tests

import (
	"context"
	"fmt"
	"time"

	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	sriovv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/dra"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/cluster"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/discovery"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/namespaces"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/network"
)

const (
	draResourceName      = "draresource"
	initialVFs           = 2
	updatedVFs           = 4
	devicePluginDSName   = "sriov-device-plugin"
	draDriverAppLabel    = "app=sriov-dra-driver"
	devicePluginAppLabel = "app=sriov-device-plugin"
)

func isDeviceClassAPIAvailable() bool {
	_, err := clients.ServerResourcesForGroupVersion(schema.GroupVersion{
		Group:   "resource.k8s.io",
		Version: "v1",
	}.String())
	return err == nil
}

// draDriverInitContainersTerminated reports whether every init container on the pod
// has terminated successfully (exit code 0). Used to assert wait-for-config has completed.
func draDriverInitContainersTerminated(pod *corev1.Pod) bool {
	if len(pod.Spec.InitContainers) == 0 {
		return true
	}
	if len(pod.Status.InitContainerStatuses) < len(pod.Spec.InitContainers) {
		return false
	}
	for _, st := range pod.Status.InitContainerStatuses {
		if st.State.Terminated == nil || st.State.Terminated.ExitCode != 0 {
			return false
		}
	}
	return true
}

func listDRADriverPods() []corev1.Pod {
	pods, err := clients.Pods(operatorNamespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: draDriverAppLabel,
	})
	Expect(err).ToNot(HaveOccurred())
	return pods.Items
}

func countAdvertisedDRADevices(nodeName, resourceAttribute string) int {
	list := &resourceapi.ResourceSliceList{}
	Expect(clients.List(context.Background(), list)).To(Succeed())

	// During a pool update the driver may publish the old and new generations together.
	// Count only the highest generation for each pool on this node.
	generation := map[string]int64{}
	for i := range list.Items {
		slice := &list.Items[i]
		if !draSliceForNode(slice, nodeName) {
			continue
		}
		if slice.Spec.Pool.Generation > generation[slice.Spec.Pool.Name] {
			generation[slice.Spec.Pool.Name] = slice.Spec.Pool.Generation
		}
	}

	count := 0
	attrKey := resourceapi.QualifiedName(dra.ResourceNameAttributeKey)
	for i := range list.Items {
		slice := &list.Items[i]
		if !draSliceForNode(slice, nodeName) || slice.Spec.Pool.Generation != generation[slice.Spec.Pool.Name] {
			continue
		}
		for _, device := range slice.Spec.Devices {
			attr, ok := device.Attributes[attrKey]
			if !ok || attr.StringValue == nil || *attr.StringValue != resourceAttribute {
				continue
			}
			count++
		}
	}
	return count
}

func draSliceForNode(slice *resourceapi.ResourceSlice, nodeName string) bool {
	return slice.Spec.Driver == consts.DRADriverBaseDeviceClassName &&
		slice.Spec.NodeName != nil &&
		*slice.Spec.NodeName == nodeName
}

func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// Ordered is required: later specs depend on earlier ones (enable DRA => create
// SriovNetworkNodePolicy / unblock init => ResourceSlices publish initial VFs =>
// NumVfs change updates ResourceSlices => disable and clean up).
var _ = Describe("[sriov] DRA", Ordered, ContinueOnFailure, func() {
	var (
		node             string
		nic              *sriovv1.InterfaceExt
		policyName       string
		deviceClassName  string
		deviceAttrsName  string
		extendedResource string
	)

	BeforeAll(func() {
		if platformType != consts.Baremetal {
			Skip("DRA operator tests require a baremetal (or emulated-PF) platform")
		}
		if !isDeviceClassAPIAvailable() {
			Skip("resource.k8s.io/v1 DeviceClass API is not available on this cluster")
		}

		err := namespaces.Create(namespaces.Test, clients)
		Expect(err).ToNot(HaveOccurred())

		err = namespaces.Clean(operatorNamespace, namespaces.Test, clients, discovery.Enabled())
		Expect(err).ToNot(HaveOccurred())

		featureFlagInitialValue := isFeatureFlagEnabled(consts.DynamicResourceAllocationFeatureGate)
		DeferCleanup(func() {
			By("Restoring dynamicResourceAllocation feature flag and cleaning test state")
			setFeatureFlag(consts.DynamicResourceAllocationFeatureGate, featureFlagInitialValue)
			WaitForSRIOVStable()
			Expect(namespaces.Clean(operatorNamespace, namespaces.Test, clients, discovery.Enabled())).To(Succeed())
		})

		sriovInfos, err := cluster.DiscoverSriov(clients, operatorNamespace)
		Expect(err).ToNot(HaveOccurred())
		node, nic, err = sriovInfos.FindOneSriovNodeAndDevice()
		Expect(err).ToNot(HaveOccurred())
		By(fmt.Sprintf("Using device %s on node %s", nic.Name, node))

		deviceClassName = dra.ResourceNameToDeviceClassName(draResourceName)
		deviceAttrsName = deviceClassName + "-attrs"
		extendedResource = "openshift.io/" + draResourceName

		// Enable DRA before creating a SriovNetworkNodePolicy. Nodes with no
		// interfaces are unblocked, so the DRA driver leaves Init without a policy.
		By("Enabling dynamicResourceAllocation feature flag")
		setFeatureFlag(consts.DynamicResourceAllocationFeatureGate, true)
	})

	It("should deploy the DRA driver DaemonSet and remove the device plugin", func() {
		By("Waiting for sriov-dra-driver DaemonSet to exist")
		Eventually(func(g Gomega) {
			ds := &appsv1.DaemonSet{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      consts.DRADriverDaemonSetName,
			}, ds)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ds.Status.DesiredNumberScheduled).To(BeNumerically(">", 0))
		}, waitingTime, 5*time.Second).Should(Succeed())

		By("Waiting for DRA driver pods to become Ready")
		Eventually(func(g Gomega) {
			pods, err := clients.Pods(operatorNamespace).List(context.Background(), metav1.ListOptions{
				LabelSelector: draDriverAppLabel,
			})
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(pods.Items).ToNot(BeEmpty())
			for _, p := range pods.Items {
				g.Expect(draDriverInitContainersTerminated(&p)).To(BeTrue(),
					"DRA driver pod %s still in Init: %v", p.Name, p.Status.InitContainerStatuses)
				g.Expect(p.Status.Phase).To(Equal(corev1.PodRunning))
				g.Expect(isPodReady(&p)).To(BeTrue(), "DRA driver pod %s is not Ready", p.Name)
			}
		}, waitingTime, 5*time.Second).Should(Succeed())

		By("Waiting for sriov-device-plugin DaemonSet to be removed")
		assertObjectIsNotFound(devicePluginDSName, &appsv1.DaemonSet{})
	})

	It("should create the base DeviceClass managed by the operator", func() {
		Eventually(func(g Gomega) {
			dc := &resourceapi.DeviceClass{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{
				Name: consts.DRADriverBaseDeviceClassName,
			}, dc)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(dc.Labels).To(HaveKeyWithValue(dra.GeneratedByLabel, consts.SriovNetworkOperatorIdentifier))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	It("should create SriovNetworkNodePolicy and unblock DRA driver init with DRA CRs", func() {
		By("Creating SriovNetworkNodePolicy for resource " + draResourceName)
		created, err := network.CreateSriovPolicy(clients, "test-dra-policy-", operatorNamespace, nic.Name, node, initialVFs, draResourceName, "netdevice")
		Expect(err).ToNot(HaveOccurred())
		policyName = created.Name
		WaitForSRIOVStable()

		By("Waiting for DRA driver pods to leave Init and become Running")
		Eventually(func(g Gomega) {
			pods, err := clients.Pods(operatorNamespace).List(context.Background(), metav1.ListOptions{
				LabelSelector: draDriverAppLabel,
			})
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(pods.Items).ToNot(BeEmpty())

			readyPods := 0
			for _, p := range pods.Items {
				g.Expect(draDriverInitContainersTerminated(&p)).To(BeTrue(),
					"DRA driver pod %s still has incomplete init containers: %v", p.Name, p.Status.InitContainerStatuses)
				g.Expect(p.Status.Phase).To(Equal(corev1.PodRunning),
					"DRA driver pod %s phase=%s", p.Name, p.Status.Phase)
				if isPodReady(&p) {
					readyPods++
				}
			}
			g.Expect(readyPods).To(BeNumerically(">", 0))
		}, waitingTime, 5*time.Second).Should(Succeed())

		By("Waiting for sriov-dra-driver DaemonSet to finish rollout")
		Eventually(func(g Gomega) {
			ds := &appsv1.DaemonSet{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      consts.DRADriverDaemonSetName,
			}, ds)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ds.Status.ObservedGeneration).To(BeNumerically(">=", ds.Generation))
			g.Expect(ds.Status.NumberReady).To(Equal(ds.Status.DesiredNumberScheduled))
			g.Expect(ds.Status.UpdatedNumberScheduled).To(Equal(ds.Status.DesiredNumberScheduled))
		}, waitingTime, 5*time.Second).Should(Succeed())

		By("Waiting for DeviceAttributes " + deviceAttrsName)
		Eventually(func(g Gomega) {
			attrs := &sriovdrav1alpha1.DeviceAttributes{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      deviceAttrsName,
			}, attrs)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(attrs.Labels).To(HaveKeyWithValue(dra.GeneratedByLabel, consts.SriovNetworkOperatorIdentifier))
			g.Expect(attrs.Labels).To(HaveKeyWithValue(dra.ResourcePoolLabel, deviceClassName))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("Waiting for SriovResourcePolicy for node " + node)
		Eventually(func(g Gomega) {
			policy := &sriovdrav1alpha1.SriovResourcePolicy{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      node,
			}, policy)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(policy.Labels).To(HaveKeyWithValue(dra.GeneratedByLabel, consts.SriovNetworkOperatorIdentifier))
			g.Expect(policy.Labels).To(HaveKeyWithValue(dra.SriovResourcePolicyNodeLabel, node))
			g.Expect(policy.Spec.Configs).ToNot(BeEmpty())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("Waiting for per-resource DeviceClass " + deviceClassName)
		Eventually(func(g Gomega) {
			dc := &resourceapi.DeviceClass{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{Name: deviceClassName}, dc)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(dc.Labels).To(HaveKeyWithValue(dra.GeneratedByLabel, consts.SriovNetworkOperatorIdentifier))
			g.Expect(dc.Labels).To(HaveKeyWithValue(dra.DeviceClassResourceNameLabel, draResourceName))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	It("should publish the initial VFs on ResourceSlices for the selected node", func() {
		By("Waiting for ResourceSlices to publish the initial VFs on " + node)
		Eventually(func(g Gomega) {
			g.Expect(countAdvertisedDRADevices(node, extendedResource)).To(Equal(initialVFs))
		}, waitingTime, 5*time.Second).Should(Succeed())

		By("Checking other nodes do not advertise this resource")
		for _, p := range listDRADriverPods() {
			if p.Spec.NodeName == "" || p.Spec.NodeName == node {
				continue
			}
			Expect(countAdvertisedDRADevices(p.Spec.NodeName, extendedResource)).To(Equal(0),
				"node %s advertised %s before a policy selected it", p.Spec.NodeName, extendedResource)
		}
	})

	It("should publish the additional VFs on ResourceSlices when policy interfaces change", func() {
		otherCounts := map[string]int{}
		for _, p := range listDRADriverPods() {
			if p.Spec.NodeName != "" && p.Spec.NodeName != node {
				otherCounts[p.Spec.NodeName] = countAdvertisedDRADevices(p.Spec.NodeName, extendedResource)
			}
		}

		By("Changing NumVfs so SriovNetworkNodeState interfaces are updated")
		Eventually(func(g Gomega) {
			policy := &sriovv1.SriovNetworkNodePolicy{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      policyName,
			}, policy)
			g.Expect(err).ToNot(HaveOccurred())
			policy.Spec.NumVfs = updatedVFs
			g.Expect(clients.Update(context.Background(), policy)).To(Succeed())
		}, 1*time.Minute, 2*time.Second).Should(Succeed())
		WaitForSRIOVStable()

		By(fmt.Sprintf("Waiting for ResourceSlice devices on %s to increase from %d to %d", node, initialVFs, updatedVFs))
		Eventually(func(g Gomega) {
			g.Expect(countAdvertisedDRADevices(node, extendedResource)).To(Equal(updatedVFs))
		}, waitingTime, 5*time.Second).Should(Succeed())

		By("Checking ResourceSlice device counts on other nodes did not change")
		for nodeName, before := range otherCounts {
			Expect(countAdvertisedDRADevices(nodeName, extendedResource)).To(Equal(before),
				"ResourceSlice devices changed on %s", nodeName)
		}
	})

	It("should clean up DRA objects and restore the device plugin when DRA is disabled", func() {
		By("Disabling dynamicResourceAllocation feature flag")
		setFeatureFlag(consts.DynamicResourceAllocationFeatureGate, false)
		WaitForSRIOVStable()

		By("Waiting for sriov-dra-driver DaemonSet to be removed")
		assertObjectIsNotFound(consts.DRADriverDaemonSetName, &appsv1.DaemonSet{})

		By("Waiting for operator-generated DeviceAttributes to be removed")
		Eventually(func(g Gomega) {
			list := &sriovdrav1alpha1.DeviceAttributesList{}
			err := clients.List(context.Background(), list,
				runtimeclient.InNamespace(operatorNamespace),
				runtimeclient.MatchingLabels(dra.OperatorGeneratedByLabels()))
			if meta.IsNoMatchError(err) {
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(list.Items).To(BeEmpty())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("Waiting for operator-generated SriovResourcePolicy to be removed")
		Eventually(func(g Gomega) {
			list := &sriovdrav1alpha1.SriovResourcePolicyList{}
			err := clients.List(context.Background(), list,
				runtimeclient.InNamespace(operatorNamespace),
				runtimeclient.MatchingLabels(dra.OperatorGeneratedByLabels()))
			if meta.IsNoMatchError(err) {
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(list.Items).To(BeEmpty())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("Waiting for operator-generated DeviceClasses to be removed")
		Eventually(func(g Gomega) {
			list := &resourceapi.DeviceClassList{}
			err := clients.List(context.Background(), list,
				runtimeclient.MatchingLabels(dra.OperatorGeneratedByLabels()))
			if meta.IsNoMatchError(err) {
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(list.Items).To(BeEmpty())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("Waiting for base DeviceClass to be removed")
		Eventually(func(g Gomega) {
			dc := &resourceapi.DeviceClass{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{
				Name: consts.DRADriverBaseDeviceClassName,
			}, dc)
			g.Expect(errors.IsNotFound(err) || meta.IsNoMatchError(err)).To(BeTrue())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("Waiting for sriov-device-plugin DaemonSet to be restored")
		Eventually(func(g Gomega) {
			ds := &appsv1.DaemonSet{}
			err := clients.Get(context.Background(), runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      devicePluginDSName,
			}, ds)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ds.Status.NumberReady).To(Equal(ds.Status.DesiredNumberScheduled))
			g.Expect(ds.Status.DesiredNumberScheduled).To(BeNumerically(">", 0))
		}, waitingTime, 5*time.Second).Should(Succeed())

		By("Waiting for at least one Running device-plugin pod")
		Eventually(func(g Gomega) {
			pods, err := clients.Pods(operatorNamespace).List(context.Background(), metav1.ListOptions{
				LabelSelector: devicePluginAppLabel,
			})
			g.Expect(err).ToNot(HaveOccurred())
			running := 0
			for _, p := range pods.Items {
				if p.Status.Phase == corev1.PodRunning {
					running++
				}
			}
			g.Expect(running).To(BeNumerically(">", 0))
		}, waitingTime, 5*time.Second).Should(Succeed())
	})
})

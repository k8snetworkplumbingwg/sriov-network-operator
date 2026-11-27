package tests

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"

	sriovv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/cluster"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/discovery"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/namespaces"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/network"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/pod"
)

const (
	draResourceName      = "draresource"
	initialVFs           = 2
	updatedVFs           = 4
	devicePluginDSName   = "sriov-device-plugin"
	draDriverAppLabel    = "app=sriov-dra-driver"
	devicePluginAppLabel = "app=sriov-device-plugin"
	// draClaimRefName is the pod-level claim reference name used by test pods.
	draClaimRefName = "claim-ref"
)

// isDeviceClassAPIAvailable reports whether resource.k8s.io/v1 is discoverable.
// It returns (false, nil) only when the API is conclusively unavailable (NotFound /
// NoMatch). Other discovery failures (auth, outage, etc.) are returned as errors.
func isDeviceClassAPIAvailable() (bool, error) {
	_, err := clients.ServerResourcesForGroupVersion(schema.GroupVersion{
		Group:   "resource.k8s.io",
		Version: "v1",
	}.String())
	if err == nil {
		return true, nil
	}
	if errors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return false, nil
	}
	return false, fmt.Errorf("discovering resource.k8s.io/v1 DeviceClass API: %w", err)
}

// draDriverInitContainersTerminated reports whether the pod has a wait-for-config
// init container and every init container has terminated successfully (exit code 0).
func draDriverInitContainersTerminated(pod *corev1.Pod) bool {
	statuses := make(map[string]corev1.ContainerStatus, len(pod.Status.InitContainerStatuses))
	for _, status := range pod.Status.InitContainerStatuses {
		statuses[status.Name] = status
	}

	hasWaitForConfig := false
	for _, initContainer := range pod.Spec.InitContainers {
		if slices.Contains(initContainer.Command, "wait-for-config") {
			hasWaitForConfig = true
		}

		status, found := statuses[initContainer.Name]
		if !found || status.State.Terminated == nil || status.State.Terminated.ExitCode != 0 {
			return false
		}
	}
	return hasWaitForConfig
}

func listDRADriverPods(ctx context.Context) []corev1.Pod {
	pods, err := clients.Pods(operatorNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: draDriverAppLabel,
	})
	Expect(err).ToNot(HaveOccurred())
	return pods.Items
}

// currentDRASlices lists the SR-IOV DRA driver's ResourceSlices. During a pool update the
// driver may publish the old and new generations together, so only the highest generation
// of each pool is returned.
func currentDRASlices(ctx context.Context) ([]*resourceapi.ResourceSlice, error) {
	list := &resourceapi.ResourceSliceList{}
	if err := clients.List(ctx, list); err != nil {
		return nil, err
	}

	generation := map[string]int64{}
	for i := range list.Items {
		slice := &list.Items[i]
		if slice.Spec.Driver == consts.DRADriverBaseDeviceClassName &&
			slice.Spec.Pool.Generation > generation[slice.Spec.Pool.Name] {
			generation[slice.Spec.Pool.Name] = slice.Spec.Pool.Generation
		}
	}

	var current []*resourceapi.ResourceSlice
	for i := range list.Items {
		slice := &list.Items[i]
		if slice.Spec.Driver == consts.DRADriverBaseDeviceClassName &&
			slice.Spec.Pool.Generation == generation[slice.Spec.Pool.Name] {
			current = append(current, slice)
		}
	}
	return current, nil
}

func countAdvertisedDRADevices(ctx context.Context, nodeName, resourceAttribute string) (int, error) {
	slices, err := currentDRASlices(ctx)
	if err != nil {
		return 0, err
	}

	count := 0
	attrKey := resourceapi.QualifiedName(consts.DRAResourceNameAttributeKey)
	for _, slice := range slices {
		if !draSliceForNode(slice, nodeName) {
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
	return count, nil
}

func draSliceForNode(slice *resourceapi.ResourceSlice, nodeName string) bool {
	return slice.Spec.Driver == consts.DRADriverBaseDeviceClassName &&
		slice.Spec.NodeName != nil &&
		*slice.Spec.NodeName == nodeName
}

// waitForDRAProviderSwitch performs the user-managed checks required before
// creating policies after a feature-gate change, including during suite cleanup.
func waitForDRAProviderSwitch(ctx context.Context, draEnabled bool) {
	selected, previous := devicePluginDSName, consts.DRADriverDaemonSetName
	if draEnabled {
		selected, previous = previous, selected
	}
	By(fmt.Sprintf("Waiting for %s removal and %s rollout before creating policies", previous, selected))
	Eventually(ctx, func(g Gomega) {
		err := clients.Get(ctx, runtimeclient.ObjectKey{Namespace: operatorNamespace, Name: previous}, &appsv1.DaemonSet{})
		g.Expect(errors.IsNotFound(err)).To(BeTrue(), "previous provider DaemonSet still exists: %v", err)
		oldPods, err := clients.Pods(operatorNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + previous})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(oldPods.Items).To(BeEmpty())

		ds := &appsv1.DaemonSet{}
		g.Expect(clients.Get(ctx, runtimeclient.ObjectKey{Namespace: operatorNamespace, Name: selected}, ds)).To(Succeed())
		g.Expect(ds.DeletionTimestamp.IsZero()).To(BeTrue())
		g.Expect(ds.Status.ObservedGeneration).To(BeNumerically(">=", ds.Generation))
		g.Expect(ds.Status.UpdatedNumberScheduled).To(Equal(ds.Status.DesiredNumberScheduled))
		g.Expect(ds.Status.NumberAvailable).To(Equal(ds.Status.DesiredNumberScheduled))
		g.Expect(ds.Status.NumberReady).To(Equal(ds.Status.DesiredNumberScheduled))
		// The device plugin may have no eligible nodes until policies exist.
		if draEnabled {
			g.Expect(ds.Status.DesiredNumberScheduled).To(BeNumerically(">", 0))
		}
		pods, err := clients.Pods(operatorNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + selected})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pods.Items).To(HaveLen(int(ds.Status.DesiredNumberScheduled)))
		for _, p := range pods.Items {
			g.Expect(p.DeletionTimestamp.IsZero()).To(BeTrue())
			g.Expect(p.Status.Phase).To(Equal(corev1.PodRunning))
			g.Expect(pod.IsReady(&p)).To(BeTrue(), "provider pod %s is not Ready", p.Name)
			if draEnabled {
				g.Expect(draDriverInitContainersTerminated(&p)).To(BeTrue(), "DRA pod %s is still in Init", p.Name)
			}
		}
	}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())
}

// testDRAVFIOExtendedResource runs after the advertisement tests, before the
// ordered suite removes the policy and switches back to the device plugin.
func testDRAVFIOExtendedResource(ctx SpecContext, policyName string) {
	class := &resourceapi.DeviceClass{}
	Expect(clients.Get(ctx, runtimeclient.ObjectKey{Name: sriovv1.ResourceNameToDeviceClassName(draResourceName)}, class)).To(Succeed())
	if class.Spec.ExtendedResourceName == nil {
		Skip("DRAExtendedResource is not enabled in the API server")
	}
	extendedResource := *class.Spec.ExtendedResourceName
	infos, err := cluster.DiscoverSriov(clients, operatorNamespace)
	Expect(err).NotTo(HaveOccurred())
	worker, nic := infos.FindOneVfioSriovDevice()
	if worker == "" {
		Skip("no VFIO-capable SR-IOV device is available")
	}
	node, err := clients.Nodes().Get(ctx, worker, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	hostname := node.Labels[corev1.LabelHostname]
	Expect(hostname).NotTo(BeEmpty())

	By("Configuring a VFIO resource through the existing DRA policy")
	Eventually(ctx, func(g Gomega) {
		policy := &sriovv1.SriovNetworkNodePolicy{}
		g.Expect(clients.Get(ctx, runtimeclient.ObjectKey{Namespace: operatorNamespace, Name: policyName}, policy)).To(Succeed())
		policy.Spec.DeviceType = consts.DeviceTypeVfioPci
		policy.Spec.NumVfs = 1
		policy.Spec.NodeSelector = map[string]string{corev1.LabelHostname: hostname}
		policy.Spec.NicSelector = sriovv1.SriovNetworkNicSelector{PfNames: []string{nic.Name}}
		g.Expect(clients.Update(ctx, policy)).To(Succeed())
	}).WithTimeout(time.Minute).WithPolling(time.Second).Should(Succeed())
	WaitForSRIOVStable()
	Eventually(ctx, func(g Gomega) {
		g.Expect(clients.Get(ctx, runtimeclient.ObjectKey{Name: class.Name}, class)).To(Succeed())
		g.Expect(class.Spec.Config).To(HaveLen(1))
		g.Expect(class.Spec.Config[0].Opaque).NotTo(BeNil())
		decoded, err := runtime.Decode(configapi.Decoder, class.Spec.Config[0].Opaque.Parameters.Raw)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(decoded.(*configapi.VfConfig).Driver).To(Equal(consts.DeviceTypeVfioPci))
		count, err := countAdvertisedDRADevices(ctx, worker, extendedResource)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(count).To(Equal(1))
	}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())

	By("Requesting a VF through limits without an explicit claim or privileged access")
	consumer := pod.GetDefinition()
	consumer.Spec.NodeSelector = map[string]string{corev1.LabelHostname: hostname}
	consumer.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(false)}
	consumer.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceName(extendedResource): resource.MustParse("1")},
		Limits:   corev1.ResourceList{corev1.ResourceName(extendedResource): resource.MustParse("1")},
	}
	Expect(clients.Create(ctx, consumer)).To(Succeed())
	DeferCleanup(func(ctx SpecContext) {
		Expect(clients.Delete(ctx, consumer)).To(Succeed())
		Eventually(ctx, func() bool {
			return errors.IsNotFound(clients.Get(ctx, runtimeclient.ObjectKeyFromObject(consumer), &corev1.Pod{}))
		}).WithTimeout(waitingTime).WithPolling(time.Second).Should(BeTrue())
	})
	Eventually(ctx, func(g Gomega) {
		g.Expect(clients.Get(ctx, runtimeclient.ObjectKeyFromObject(consumer), consumer)).To(Succeed())
		g.Expect(pod.AssertContainerReady(*consumer, consumer.Spec.Containers[0].Name)).To(Succeed())
		g.Expect(consumer.Status.ExtendedResourceClaimStatus).NotTo(BeNil())
	}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())
	claim := &resourceapi.ResourceClaim{}
	Expect(clients.Get(ctx, runtimeclient.ObjectKey{Namespace: namespaces.Test,
		Name: consumer.Status.ExtendedResourceClaimStatus.ResourceClaimName}, claim)).To(Succeed())
	Expect(claim.Status.Allocation).NotTo(BeNil())
	Expect(claim.Status.Allocation.Devices.Results).To(HaveLen(1))
	Expect(claim.Status.Allocation.Devices.Config).To(ContainElement(SatisfyAll(
		HaveField("Source", resourceapi.AllocationConfigSourceClass),
		HaveField("DeviceConfiguration.Opaque.Driver", consts.DRADriverBaseDeviceClassName),
	)))

	By("Checking the claim CDI specification includes both VFIO character devices")
	cdiRoot := ""
	for _, driverPod := range listDRADriverPods(ctx) {
		if driverPod.Spec.NodeName != worker {
			continue
		}
		for _, volume := range driverPod.Spec.Volumes {
			if volume.Name == "cdi-root" && volume.HostPath != nil {
				cdiRoot = volume.HostPath.Path
			}
		}
	}
	Expect(cdiRoot).NotTo(BeEmpty())
	execCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// Read from the config daemon's host mount, without exposing host /dev to
	// the consumer. Pass paths as shell arguments rather than interpolating them.
	contents, stderr, err := runCommandOnConfigDaemonWithContext(execCtx, worker, "sh", "-c",
		`for f in "$1"/*"$2"*; do if [ -f "$f" ]; then cat "$f"; exit; fi; done; exit 1`,
		"read-cdi", filepath.Join("/host", cdiRoot), string(claim.UID))
	Expect(err).NotTo(HaveOccurred(), stderr)
	var spec struct {
		Devices []struct {
			ContainerEdits struct {
				DeviceNodes []struct {
					Path string `json:"path"`
					Type string `json:"type"`
				} `json:"deviceNodes"`
			} `json:"containerEdits"`
		} `json:"devices"`
	}
	Expect(yaml.Unmarshal([]byte(contents), &spec)).To(Succeed())
	Expect(spec.Devices).To(HaveLen(1))
	paths := []string{}
	groupPath := ""
	for _, device := range spec.Devices[0].ContainerEdits.DeviceNodes {
		if strings.HasPrefix(device.Path, "/dev/vfio/") {
			Expect(device.Type).To(Equal("c"))
			paths = append(paths, device.Path)
			if device.Path != "/dev/vfio/vfio" && !strings.Contains(strings.TrimPrefix(device.Path, "/dev/vfio/"), "/") {
				groupPath = device.Path
			}
		}
	}
	Expect(paths).To(ContainElement("/dev/vfio/vfio"))
	Expect(groupPath).NotTo(BeEmpty())
	_, stderr, err = pod.ExecCommandWithContext(execCtx, clients, consumer, "sh", "-c",
		`test -c /dev/vfio/vfio && test -c "$1" && exec 3<>/dev/vfio/vfio && exec 4<>"$1"`, "check-vfio", groupPath)
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("VFIO device access failed: %s", stderr))
}

// cleanDRAClaims deletes the ResourceClaims and ResourceClaimTemplates of the namespace.
// Callers remove the pods consuming them first.
func cleanDRAClaims(ctx context.Context, namespace string) {
	// Best effort: this runs during teardown, so failures are logged rather than asserted.
	logErr := func(action string, err error) {
		if err != nil && !errors.IsNotFound(err) {
			GinkgoWriter.Printf("DRA cleanup: %s in namespace %s failed: %v\n", action, namespace, err)
		}
	}

	claimList := &resourceapi.ResourceClaimList{}
	err := clients.List(ctx, claimList, runtimeclient.InNamespace(namespace))
	logErr("listing ResourceClaims", err)
	for i := range claimList.Items {
		logErr("deleting ResourceClaim "+claimList.Items[i].Name, clients.Delete(ctx, &claimList.Items[i]))
	}

	templateList := &resourceapi.ResourceClaimTemplateList{}
	err = clients.List(ctx, templateList, runtimeclient.InNamespace(namespace))
	logErr("listing ResourceClaimTemplates", err)
	for i := range templateList.Items {
		logErr("deleting ResourceClaimTemplate "+templateList.Items[i].Name, clients.Delete(ctx, &templateList.Items[i]))
	}
}

// deleteAndWait deletes obj (identified by its name and namespace) and waits until it is gone.
// A missing object is not an error.
func deleteAndWait(ctx context.Context, obj runtimeclient.Object, opts ...runtimeclient.DeleteOption) {
	key := runtimeclient.ObjectKeyFromObject(obj)
	Expect(runtimeclient.IgnoreNotFound(clients.Delete(ctx, obj, opts...))).To(Succeed())
	Eventually(ctx, func(g Gomega) {
		err := clients.Get(ctx, key, obj)
		g.Expect(errors.IsNotFound(err)).To(BeTrue(), "%T %s still exists (err: %v)", obj, key, err)
	}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
}

// deletePodSync deletes the pod gracefully: a forced deletion removes the object without
// waiting for the kubelet, which would release its claims while the container may still run.
func deletePodSync(ctx context.Context, namespace, name string) {
	deleteAndWait(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}})
}

func deleteClaimSync(ctx context.Context, namespace, name string) {
	deleteAndWait(ctx, &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}})
}

func deleteClaimTemplateSync(ctx context.Context, namespace, name string) {
	deleteAndWait(ctx, &resourceapi.ResourceClaimTemplate{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}})
}

func deletePolicySync(ctx context.Context, name string) {
	deleteAndWait(ctx, &sriovv1.SriovNetworkNodePolicy{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: name}})
}

func isOperatorWebhookEnabled(ctx context.Context) bool {
	cfg := &sriovv1.SriovOperatorConfig{}
	Expect(clients.Get(ctx, runtimeclient.ObjectKey{Name: "default", Namespace: operatorNamespace}, cfg)).To(Succeed())
	return cfg.Spec.EnableOperatorWebhook
}

// draResourceNameSelector returns a CEL expression selecting devices by SR-IOV resource name.
// The has() guard keeps a device without the attribute from aborting the allocation, so
// "no device matches" is not confused with an evaluation error.
func draResourceNameSelector(resourceName string) string {
	domain, name, _ := strings.Cut(consts.DRAResourceNameAttributeKey, "/")
	return fmt.Sprintf(`has(device.attributes["%s"].%s) && device.attributes["%s"].%s == "%s"`,
		domain, name, domain, name, resourceName)
}

// newDRAClaimSpec builds a ResourceClaimSpec with a single request for count devices of
// deviceClassName, optionally narrowed by a CEL expression.
func newDRAClaimSpec(deviceClassName string, count int64, celExpression string) resourceapi.ResourceClaimSpec {
	request := &resourceapi.ExactDeviceRequest{
		DeviceClassName: deviceClassName,
		Count:           count,
	}
	if celExpression != "" {
		request.Selectors = []resourceapi.DeviceSelector{
			{CEL: &resourceapi.CELDeviceSelector{Expression: celExpression}},
		}
	}
	return resourceapi.ResourceClaimSpec{
		Devices: resourceapi.DeviceClaim{
			Requests: []resourceapi.DeviceRequest{{Name: "req-device", Exactly: request}},
		},
	}
}

func createDRAClaim(ctx context.Context, name, deviceClassName string, count int64, celExpression string) {
	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespaces.Test},
		Spec:       newDRAClaimSpec(deviceClassName, count, celExpression),
	}
	Expect(clients.Create(ctx, claim)).To(Succeed())
}

// createDRAPod creates a test pod pinned to node whose container consumes the given claim
// source (a ResourceClaim or a ResourceClaimTemplate) under the draClaimRefName reference.
func createDRAPod(ctx context.Context, name, node string, claimSource corev1.PodResourceClaim) *corev1.Pod {
	claimSource.Name = draClaimRefName
	podObj := pod.GetDefinition()
	podObj.Name = name
	podObj.GenerateName = ""
	podObj.Namespace = namespaces.Test
	podObj.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": node}
	podObj.Spec.ResourceClaims = []corev1.PodResourceClaim{claimSource}
	podObj.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{{Name: draClaimRefName}}
	created, err := clients.Pods(namespaces.Test).Create(ctx, podObj, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	return created
}

func isClaimReservedFor(claim *resourceapi.ResourceClaim, uid types.UID) bool {
	for _, res := range claim.Status.ReservedFor {
		if res.UID == uid {
			return true
		}
	}
	return false
}

// waitForDRAClaimAllocated waits until the claim is allocated count distinct devices from the
// SR-IOV DRA driver.
func waitForDRAClaimAllocated(ctx context.Context, name string, count int) {
	Eventually(ctx, func(g Gomega) {
		c := &resourceapi.ResourceClaim{}
		g.Expect(clients.Get(ctx, runtimeclient.ObjectKey{Namespace: namespaces.Test, Name: name}, c)).To(Succeed())
		g.Expect(c.Status.Allocation).ToNot(BeNil())
		g.Expect(c.Status.Allocation.Devices.Results).To(HaveLen(count))
		devices := map[string]struct{}{}
		for _, result := range c.Status.Allocation.Devices.Results {
			g.Expect(result.Driver).To(Equal(consts.DRADriverBaseDeviceClassName))
			g.Expect(result.Device).ToNot(BeEmpty())
			g.Expect(result.Pool).ToNot(BeEmpty())
			devices[result.Pool+"/"+result.Device] = struct{}{}
		}
		g.Expect(devices).To(HaveLen(count), "Allocated devices must be distinct")
	}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())
}

func waitForDRAClaimReservedFor(ctx context.Context, name string, podUID types.UID) {
	Eventually(ctx, func(g Gomega) {
		c := &resourceapi.ResourceClaim{}
		g.Expect(clients.Get(ctx, runtimeclient.ObjectKey{Namespace: namespaces.Test, Name: name}, c)).To(Succeed())
		g.Expect(isClaimReservedFor(c, podUID)).To(BeTrue(), "ResourceClaim %s not reserved for pod %s", name, podUID)
	}).WithTimeout(1 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
}

func waitForPodRunningOnNode(ctx context.Context, name, node string) *corev1.Pod {
	var running *corev1.Pod
	Eventually(ctx, func(g Gomega) {
		p, err := clients.Pods(namespaces.Test).Get(ctx, name, metav1.GetOptions{})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p.Spec.NodeName).To(Equal(node))
		g.Expect(p.Status.Phase).To(Equal(corev1.PodRunning))
		// Callers exec into the container, so Running alone is not enough.
		g.Expect(pod.AssertContainerReady(*p, p.Spec.Containers[0].Name)).To(Succeed())
		running = p
	}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())
	return running
}

// expectDRADevicesInPod verifies that every device allocated to the claim is published by the
// driver with the expected SR-IOV resource name, and that the driver exposed it to the pod's
// container through the SRIOVNETWORK_VF_DEVICE_<device> variable set by its CDI edits.
func expectDRADevicesInPod(ctx context.Context, p *corev1.Pod, claimName, resourceName string) {
	claim := &resourceapi.ResourceClaim{}
	Expect(clients.Get(ctx, runtimeclient.ObjectKey{Namespace: namespaces.Test, Name: claimName}, claim)).To(Succeed())
	Expect(claim.Status.Allocation).ToNot(BeNil())

	slices, err := currentDRASlices(ctx)
	Expect(err).ToNot(HaveOccurred())
	published := map[string]string{}
	attrKey := resourceapi.QualifiedName(consts.DRAResourceNameAttributeKey)
	for _, slice := range slices {
		for _, device := range slice.Spec.Devices {
			if attr, ok := device.Attributes[attrKey]; ok && attr.StringValue != nil {
				published[slice.Spec.Pool.Name+"/"+device.Name] = *attr.StringValue
			}
		}
	}

	stdout, stderr, err := pod.ExecCommandWithContext(ctx, clients, p, "env")
	Expect(err).ToNot(HaveOccurred(), stderr)
	env := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			env[key] = value
		}
	}

	for _, result := range claim.Status.Allocation.Devices.Results {
		Expect(published).To(HaveKeyWithValue(result.Pool+"/"+result.Device, resourceName),
			"device %s/%s allocated to ResourceClaim %s is not published as %s", result.Pool, result.Device, claimName, resourceName)
		Expect(env).To(HaveKeyWithValue("SRIOVNETWORK_VF_DEVICE_"+strings.ReplaceAll(result.Device, "-", "_"), Not(BeEmpty())),
			"device %s allocated to ResourceClaim %s is not exposed to pod %s", result.Device, claimName, p.Name)
	}
}

// newDRATestPolicy returns a policy selecting the given PF on node, to be customized by the
// admission webhook specs.
func newDRATestPolicy(name, node, pfName, resourceName string) *sriovv1.SriovNetworkNodePolicy {
	return &sriovv1.SriovNetworkNodePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: operatorNamespace},
		Spec: sriovv1.SriovNetworkNodePolicySpec{
			NodeSelector: map[string]string{"kubernetes.io/hostname": node},
			NumVfs:       initialVFs,
			ResourceName: resourceName,
			Priority:     99,
			NicSelector:  sriovv1.SriovNetworkNicSelector{PfNames: []string{pfName}},
			DeviceType:   consts.DeviceTypeNetDevice,
		},
	}
}

// expectDRAPolicyRejected creates the policy and expects the admission webhook to reject it
// with a message containing reason.
func expectDRAPolicyRejected(ctx context.Context, policy *sriovv1.SriovNetworkNodePolicy, reason string) {
	DeferCleanup(func(cleanupCtx context.Context) {
		// Only reached with an existing policy when the webhook wrongly admitted it:
		// remove it and let the nodes settle before the next spec.
		err := clients.Delete(cleanupCtx, policy)
		if err == nil {
			WaitForSRIOVStable()
			return
		}
		Expect(runtimeclient.IgnoreNotFound(err)).To(Succeed())
	})

	err := clients.Create(ctx, policy)
	Expect(err).To(HaveOccurred(), "Admission webhook must reject policy %s", policy.Name)
	Expect(err.Error()).To(ContainSubstring(reason))
}

// expectDRAPodUnschedulable waits until the scheduler has reported the pod as not schedulable
// (so the check below is not trivially true for a pod it has not looked at yet), then verifies
// the pod stays Pending and its claim stays unallocated.
func expectDRAPodUnschedulable(ctx context.Context, podName, claimName string) {
	Eventually(ctx, func(g Gomega) {
		p, err := clients.Pods(namespaces.Test).Get(ctx, podName, metav1.GetOptions{})
		g.Expect(err).ToNot(HaveOccurred())
		rejected := false
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
				rejected = true
			}
		}
		g.Expect(rejected).To(BeTrue(), "scheduler has not reported pod %s as unschedulable: %v", podName, p.Status.Conditions)
	}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())

	Consistently(ctx, func(g Gomega) {
		c := &resourceapi.ResourceClaim{}
		g.Expect(clients.Get(ctx, runtimeclient.ObjectKey{Namespace: namespaces.Test, Name: claimName}, c)).To(Succeed())
		g.Expect(c.Status.Allocation).To(BeNil())

		p, err := clients.Pods(namespaces.Test).Get(ctx, podName, metav1.GetOptions{})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p.Spec.NodeName).To(BeEmpty())
		g.Expect(p.Status.Phase).To(Equal(corev1.PodPending))
	}).WithTimeout(15 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
}

// Ordered is required: later specs depend on earlier ones (enable DRA => create
// SriovNetworkNodePolicy / unblock init => ResourceSlices publish initial VFs =>
// NumVfs change updates ResourceSlices => pods allocate the VFs through
// ResourceClaims / ResourceClaimTemplates => a second policy is created and deleted
// while DRA stays active => VFIO extended-resource consumer => drain on policy
// deletion => disable and clean up).
var _ = Describe("[sriov] DRA", Ordered, func() {
	var (
		node             string
		nic              *sriovv1.InterfaceExt
		policyName       string
		deviceClassName  string
		deviceAttrsName  string
		extendedResource string
	)

	BeforeAll(func(ctx SpecContext) {
		if discovery.Enabled() {
			Skip("DRA conformance suite changes the resource provider, which is unsuitable for discovery mode")
		}
		if platformType != consts.Baremetal {
			Skip("DRA operator tests require a baremetal (or emulated-PF) platform")
		}
		available, err := isDeviceClassAPIAvailable()
		Expect(err).ToNot(HaveOccurred())
		if !available {
			Skip("resource.k8s.io/v1 DeviceClass API is not available on this cluster")
		}

		err = namespaces.Create(namespaces.Test, clients)
		Expect(err).ToNot(HaveOccurred())

		err = namespaces.Clean(operatorNamespace, namespaces.Test, clients, discovery.Enabled())
		Expect(err).ToNot(HaveOccurred())
		WaitForSRIOVStable()

		// An interrupted run leaves its fixed-name claims behind, and namespaces.Clean
		// does not remove them.
		By("Removing ResourceClaims and ResourceClaimTemplates left by a previous run")
		Eventually(ctx, func(g Gomega) {
			// cleanDRAClaims is best effort, so repeat it until nothing is left.
			cleanDRAClaims(ctx, namespaces.Test)
			claims := &resourceapi.ResourceClaimList{}
			g.Expect(clients.List(ctx, claims, runtimeclient.InNamespace(namespaces.Test))).To(Succeed())
			g.Expect(claims.Items).To(BeEmpty())
			templates := &resourceapi.ResourceClaimTemplateList{}
			g.Expect(clients.List(ctx, templates, runtimeclient.InNamespace(namespaces.Test))).To(Succeed())
			g.Expect(templates.Items).To(BeEmpty())
		}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		featureFlagInitialValue := isFeatureFlagEnabled(consts.DynamicResourceAllocationFeatureGate)
		DeferCleanup(func(ctx SpecContext) {
			By("Restoring dynamicResourceAllocation feature flag and cleaning test state")
			// Switching providers in either direction requires removing policies first.
			if err := namespaces.CleanPods(namespaces.Test, clients); err != nil {
				AddReportEntry("DRA teardown pod cleanup failed", err.Error())
			}
			cleanDRAClaims(ctx, namespaces.Test)
			if err := namespaces.CleanNetworks(operatorNamespace, clients); err != nil {
				AddReportEntry("DRA teardown network cleanup failed", err.Error())
			}
			if err := namespaces.CleanPolicies(operatorNamespace, clients); err != nil {
				AddReportEntry("DRA teardown policy cleanup failed", err.Error())
			}
			if err := namespaces.CleanPools(operatorNamespace, clients); err != nil {
				AddReportEntry("DRA teardown pool cleanup failed", err.Error())
			}
			Eventually(func(g Gomega) {
				policies := &sriovv1.SriovNetworkNodePolicyList{}
				g.Expect(clients.List(context.Background(), policies,
					runtimeclient.InNamespace(operatorNamespace))).To(Succeed())
				for _, policy := range policies.Items {
					g.Expect(policy.Name == "default" || !strings.HasPrefix(policy.Name, "test-")).To(BeTrue(),
						"test policy %q has not been removed", policy.Name)
				}
			}).WithTimeout(waitingTime).WithPolling(time.Second).Should(Succeed())
			WaitForSRIOVStable()
			setFeatureFlag(consts.DynamicResourceAllocationFeatureGate, featureFlagInitialValue)
			WaitForSRIOVStable()
			waitForDRAProviderSwitch(ctx, featureFlagInitialValue)
		})

		// Assign the package-level sriovInfos (no :=) so later specs use this discovery.
		sriovInfos, err = cluster.DiscoverSriov(clients, operatorNamespace)
		Expect(err).ToNot(HaveOccurred())
		node, nic, err = sriovInfos.FindOneSriovNodeAndDevice()
		if err == nil && (nic.TotalVfs < initialVFs || nic.TotalVfs < updatedVFs) {
			for _, candidateNode := range sriovInfos.Nodes {
				devices, err := sriovInfos.FindSriovDevices(candidateNode)
				Expect(err).ToNot(HaveOccurred())
				for _, candidateNIC := range devices {
					if candidateNIC.TotalVfs >= initialVFs && candidateNIC.TotalVfs >= updatedVFs {
						node, nic = candidateNode, candidateNIC
						break
					}
				}
				if nic.TotalVfs >= initialVFs && nic.TotalVfs >= updatedVFs {
					break
				}
			}
		}
		if err != nil || nic.TotalVfs < initialVFs || nic.TotalVfs < updatedVFs {
			Skip(fmt.Sprintf("DRA conformance suite requires a PF with at least %d total VFs", updatedVFs))
		}
		By(fmt.Sprintf("Using device %s on node %s", nic.Name, node))

		deviceClassName = sriovv1.ResourceNameToDeviceClassName(draResourceName)
		deviceAttrsName = deviceClassName + "-attrs"

		// Enable DRA before creating a SriovNetworkNodePolicy. Nodes with no
		// interfaces are unblocked, so the DRA driver leaves Init without a policy.
		By("Enabling dynamicResourceAllocation feature flag")
		setFeatureFlag(consts.DynamicResourceAllocationFeatureGate, true)
	})

	It("should deploy the DRA driver DaemonSet and remove the device plugin", func(ctx SpecContext) {
		waitForDRAProviderSwitch(ctx, true)
	})

	It("should create the base DeviceClass managed by the operator", func(ctx SpecContext) {
		Eventually(ctx, func(g Gomega) {
			dc := &resourceapi.DeviceClass{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Name: consts.DRADriverBaseDeviceClassName,
			}, dc)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(dc.Labels).To(HaveKeyWithValue(consts.DRAGeneratedByLabel, consts.SriovNetworkOperatorIdentifier))
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
	})

	It("should create SriovNetworkNodePolicy and unblock DRA driver init with DRA CRs", func(ctx SpecContext) {
		By("Creating SriovNetworkNodePolicy for resource " + draResourceName)
		created, err := network.CreateSriovPolicy(clients, "test-dra-policy-", operatorNamespace, nic.Name, node, initialVFs, draResourceName, "netdevice")
		Expect(err).ToNot(HaveOccurred())
		policyName = created.Name
		WaitForSRIOVStable()

		By("Waiting for DRA driver pods to leave Init and become Running")
		Eventually(ctx, func(g Gomega) {
			pods, err := clients.Pods(operatorNamespace).List(ctx, metav1.ListOptions{
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
				if pod.IsReady(&p) {
					readyPods++
				}
			}
			g.Expect(readyPods).To(BeNumerically(">", 0))
		}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for sriov-dra-driver DaemonSet to finish rollout")
		Eventually(ctx, func(g Gomega) {
			ds := &appsv1.DaemonSet{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      consts.DRADriverDaemonSetName,
			}, ds)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ds.Status.ObservedGeneration).To(BeNumerically(">=", ds.Generation))
			g.Expect(ds.Status.NumberReady).To(Equal(ds.Status.DesiredNumberScheduled))
			g.Expect(ds.Status.UpdatedNumberScheduled).To(Equal(ds.Status.DesiredNumberScheduled))
		}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for DeviceAttributes " + deviceAttrsName)
		attrs := &sriovdrav1alpha1.DeviceAttributes{}
		Eventually(ctx, func(g Gomega) {
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      deviceAttrsName,
			}, attrs)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(attrs.Labels).To(HaveKeyWithValue(consts.DRAGeneratedByLabel, consts.SriovNetworkOperatorIdentifier))
			g.Expect(attrs.Labels).To(HaveKeyWithValue(consts.DRAResourcePoolLabel, deviceClassName))
			attribute, ok := attrs.Spec.Attributes[resourceapi.QualifiedName(consts.DRAResourceNameAttributeKey)]
			g.Expect(ok).To(BeTrue())
			g.Expect(attribute.StringValue).NotTo(BeNil())
			if ok && attribute.StringValue != nil {
				extendedResource = *attribute.StringValue
			}
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for SriovResourcePolicy for node " + node)
		Eventually(ctx, func(g Gomega) {
			policy := &sriovdrav1alpha1.SriovResourcePolicy{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      node,
			}, policy)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(policy.Labels).To(HaveKeyWithValue(consts.DRAGeneratedByLabel, consts.SriovNetworkOperatorIdentifier))
			g.Expect(policy.Labels).To(HaveKeyWithValue(consts.DRASriovResourcePolicyNodeLabel, node))
			g.Expect(policy.Spec.Configs).ToNot(BeEmpty())
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for per-resource DeviceClass " + deviceClassName)
		Eventually(ctx, func(g Gomega) {
			dc := &resourceapi.DeviceClass{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{Name: deviceClassName}, dc)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(dc.Labels).To(HaveKeyWithValue(consts.DRAGeneratedByLabel, consts.SriovNetworkOperatorIdentifier))
			g.Expect(dc.Labels).To(HaveKeyWithValue(consts.DRADeviceClassResourceNameLabel, draResourceName))
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
	})

	It("should reject policies whose resourceName collides after DRA normalization via admission webhook", func(ctx SpecContext) {
		if !isOperatorWebhookEnabled(ctx) {
			Skip("Operator admission webhook is disabled (enableOperatorWebhook=false)")
		}

		collidingResourceName := strings.ToUpper(draResourceName)
		By("Attempting to create a policy with resourceName " + collidingResourceName + " colliding with " + draResourceName)
		expectDRAPolicyRejected(ctx,
			newDRATestPolicy("test-dra-colliding-policy", node, nic.Name, collidingResourceName),
			"normalizes to the same DRA device class name")
	})

	It("should reject policies that cannot be rendered in DRA mode via admission webhook", func(ctx SpecContext) {
		if !isOperatorWebhookEnabled(ctx) {
			Skip("Operator admission webhook is disabled (enableOperatorWebhook=false)")
		}

		By("Attempting to create a policy using a netFilter selector")
		netFilterPolicy := newDRATestPolicy("test-dra-netfilter-policy", node, nic.Name, "testdranetfilter")
		// netFilter cannot be combined with the other NIC selector fields.
		netFilterPolicy.Spec.NicSelector = sriovv1.SriovNetworkNicSelector{NetFilter: "openstack/NetworkID:e48c7670-bd42-4eb8-ac0b-2f6f9a1c1a52"}
		expectDRAPolicyRejected(ctx, netFilterPolicy, "netFilter selector is not supported in DRA mode")

		By("Attempting to create a policy selecting a VF range")
		expectDRAPolicyRejected(ctx,
			newDRATestPolicy("test-dra-vf-range-policy", node, nic.Name+"#0-0", "testdravfrange"),
			"VF range in pfNames selector")

		By("Attempting to create a vfio-pci policy sharing resource " + draResourceName + " with a netdevice policy")
		vfioPolicy := newDRATestPolicy("test-dra-mixed-type-policy", node, nic.Name, draResourceName)
		vfioPolicy.Spec.DeviceType = consts.DeviceTypeVfioPci
		expectDRAPolicyRejected(ctx, vfioPolicy, "vfio-pci and netdevice cannot share a DeviceClass")
	})

	It("should refuse to switch resource providers while policies exist via admission webhook", func(ctx SpecContext) {
		if !isOperatorWebhookEnabled(ctx) {
			Skip("Operator admission webhook is disabled (enableOperatorWebhook=false)")
		}

		cfg := &sriovv1.SriovOperatorConfig{}
		Expect(clients.Get(ctx, runtimeclient.ObjectKey{Name: "default", Namespace: operatorNamespace}, cfg)).To(Succeed())
		cfg.Spec.FeatureGates[consts.DynamicResourceAllocationFeatureGate] = false

		DeferCleanup(func() {
			// Only changes something when the webhook wrongly admitted the update.
			if !isFeatureFlagEnabled(consts.DynamicResourceAllocationFeatureGate) {
				setFeatureFlag(consts.DynamicResourceAllocationFeatureGate, true)
				WaitForSRIOVStable()
			}
		})

		By("Attempting to disable dynamicResourceAllocation while policy " + policyName + " exists")
		err := clients.Update(ctx, cfg)
		Expect(err).To(HaveOccurred(), "Admission webhook must reject the provider switch")
		Expect(err.Error()).To(ContainSubstring("remove all policies before switching providers"))
	})

	It("should publish the initial VFs on ResourceSlices for the selected node", func(ctx SpecContext) {
		By("Waiting for ResourceSlices to publish the initial VFs on " + node)
		Eventually(ctx, func(g Gomega) {
			count, err := countAdvertisedDRADevices(ctx, node, extendedResource)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(count).To(Equal(initialVFs))
		}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())

		By("Checking other nodes do not advertise this resource")
		for _, p := range listDRADriverPods(ctx) {
			if p.Spec.NodeName == "" || p.Spec.NodeName == node {
				continue
			}
			count, err := countAdvertisedDRADevices(ctx, p.Spec.NodeName, extendedResource)
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(0),
				"node %s advertised %s before a policy selected it", p.Spec.NodeName, extendedResource)
		}
	})

	It("should publish the additional VFs on ResourceSlices when policy interfaces change", func(ctx SpecContext) {
		otherCounts := map[string]int{}
		for _, p := range listDRADriverPods(ctx) {
			if p.Spec.NodeName != "" && p.Spec.NodeName != node {
				count, err := countAdvertisedDRADevices(ctx, p.Spec.NodeName, extendedResource)
				Expect(err).ToNot(HaveOccurred())
				otherCounts[p.Spec.NodeName] = count
			}
		}

		By("Changing NumVfs so SriovNetworkNodeState interfaces are updated")
		Eventually(ctx, func(g Gomega) {
			policy := &sriovv1.SriovNetworkNodePolicy{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      policyName,
			}, policy)
			g.Expect(err).ToNot(HaveOccurred())
			policy.Spec.NumVfs = updatedVFs
			g.Expect(clients.Update(ctx, policy)).To(Succeed())
		}).WithTimeout(1 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
		WaitForSRIOVStable()

		By(fmt.Sprintf("Waiting for ResourceSlice devices on %s to increase from %d to %d", node, initialVFs, updatedVFs))
		Eventually(ctx, func(g Gomega) {
			count, err := countAdvertisedDRADevices(ctx, node, extendedResource)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(count).To(Equal(updatedVFs))
		}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())

		By("Checking ResourceSlice device counts on other nodes did not change")
		for nodeName, before := range otherCounts {
			count, err := countAdvertisedDRADevices(ctx, nodeName, extendedResource)
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(before),
				"ResourceSlice devices changed on %s", nodeName)
		}
	})

	It("should allocate SR-IOV resources to a pod via ResourceClaim using the per-resource DeviceClass", func(ctx SpecContext) {
		claimName := "test-dra-claim"
		podName := "test-dra-claim-pod"

		DeferCleanup(func(cleanupCtx context.Context) {
			deletePodSync(cleanupCtx, namespaces.Test, podName)
			deleteClaimSync(cleanupCtx, namespaces.Test, claimName)
		})

		By("Creating a ResourceClaim for DeviceClass " + deviceClassName)
		createDRAClaim(ctx, claimName, deviceClassName, 1, "")

		By("Creating a pod consuming the ResourceClaim")
		createdPod := createDRAPod(ctx, podName, node, corev1.PodResourceClaim{ResourceClaimName: ptr.To(claimName)})

		By("Waiting for the ResourceClaim to be allocated")
		waitForDRAClaimAllocated(ctx, claimName, 1)

		By("Verifying the ResourceClaim is reserved for the pod")
		waitForDRAClaimReservedFor(ctx, claimName, createdPod.UID)

		By("Waiting for the pod to be Running on " + node)
		runningPod := waitForPodRunningOnNode(ctx, podName, node)

		By("Verifying the allocated device is exposed to the pod")
		expectDRADevicesInPod(ctx, runningPod, claimName, extendedResource)

		By("Deleting the pod and verifying reservation is released")
		deletePodSync(ctx, namespaces.Test, podName)

		Eventually(ctx, func(g Gomega) {
			c := &resourceapi.ResourceClaim{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{Namespace: namespaces.Test, Name: claimName}, c)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(isClaimReservedFor(c, createdPod.UID)).To(BeFalse(),
				"ResourceClaim %s still reserved for deleted pod %s", claimName, createdPod.UID)
		}).WithTimeout(1 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
	})

	It("should allocate SR-IOV resources to a pod via ResourceClaimTemplate", func(ctx SpecContext) {
		templateName := "test-dra-template"
		podName := "test-dra-template-pod"

		DeferCleanup(func(cleanupCtx context.Context) {
			deletePodSync(cleanupCtx, namespaces.Test, podName)
			deleteClaimTemplateSync(cleanupCtx, namespaces.Test, templateName)
		})

		By("Creating a ResourceClaimTemplate for DeviceClass " + deviceClassName)
		template := &resourceapi.ResourceClaimTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      templateName,
				Namespace: namespaces.Test,
			},
			Spec: resourceapi.ResourceClaimTemplateSpec{
				Spec: newDRAClaimSpec(deviceClassName, 1, ""),
			},
		}
		Expect(clients.Create(ctx, template)).To(Succeed())

		By("Creating a pod referencing the ResourceClaimTemplate")
		createdPod := createDRAPod(ctx, podName, node, corev1.PodResourceClaim{ResourceClaimTemplateName: ptr.To(templateName)})

		By("Waiting for the pod to be Running and checking generated ResourceClaim")
		runningPod := waitForPodRunningOnNode(ctx, podName, node)
		generatedClaimName := ""
		for _, status := range runningPod.Status.ResourceClaimStatuses {
			if status.Name == draClaimRefName && status.ResourceClaimName != nil {
				generatedClaimName = *status.ResourceClaimName
			}
		}
		Expect(generatedClaimName).ToNot(BeEmpty(), "pod %s has no generated ResourceClaim for %s: %v",
			podName, draClaimRefName, runningPod.Status.ResourceClaimStatuses)

		By("Verifying generated ResourceClaim " + generatedClaimName + " is allocated and reserved for the pod")
		waitForDRAClaimAllocated(ctx, generatedClaimName, 1)
		waitForDRAClaimReservedFor(ctx, generatedClaimName, createdPod.UID)

		By("Verifying the allocated device is exposed to the pod")
		expectDRADevicesInPod(ctx, runningPod, generatedClaimName, extendedResource)

		By("Deleting the pod and verifying generated ResourceClaim is garbage-collected")
		deletePodSync(ctx, namespaces.Test, podName)

		Eventually(ctx, func(g Gomega) {
			c := &resourceapi.ResourceClaim{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{Namespace: namespaces.Test, Name: generatedClaimName}, c)
			g.Expect(errors.IsNotFound(err)).To(BeTrue(), "generated ResourceClaim %s still exists", generatedClaimName)
		}).WithTimeout(1 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
	})

	It("should allocate SR-IOV resources using the base DeviceClass with a CEL selector", func(ctx SpecContext) {
		claimName := "test-dra-base-claim"
		podName := "test-dra-base-pod"

		DeferCleanup(func(cleanupCtx context.Context) {
			deletePodSync(cleanupCtx, namespaces.Test, podName)
			deleteClaimSync(cleanupCtx, namespaces.Test, claimName)
		})

		By("Creating a ResourceClaim referencing base DeviceClass with CEL selector")
		createDRAClaim(ctx, claimName, consts.DRADriverBaseDeviceClassName, 1, draResourceNameSelector(extendedResource))

		By("Creating a pod consuming the base DeviceClass ResourceClaim")
		createDRAPod(ctx, podName, node, corev1.PodResourceClaim{ResourceClaimName: ptr.To(claimName)})

		By("Waiting for the pod to be Running and claim allocated")
		runningPod := waitForPodRunningOnNode(ctx, podName, node)
		waitForDRAClaimAllocated(ctx, claimName, 1)

		By("Verifying the selected device belongs to resource " + extendedResource)
		expectDRADevicesInPod(ctx, runningPod, claimName, extendedResource)
	})

	It("should not schedule a pod when a CEL selector matches no available devices", func(ctx SpecContext) {
		claimName := "test-dra-unmatched-cel-claim"
		podName := "test-dra-unmatched-cel-pod"

		DeferCleanup(func(cleanupCtx context.Context) {
			deletePodSync(cleanupCtx, namespaces.Test, podName)
			deleteClaimSync(cleanupCtx, namespaces.Test, claimName)
		})

		By("Creating a ResourceClaim referencing base DeviceClass with an unmatchable CEL selector")
		createDRAClaim(ctx, claimName, consts.DRADriverBaseDeviceClassName, 1, draResourceNameSelector("nonexistent-resource"))

		By("Creating a pod consuming the unmatchable ResourceClaim")
		createDRAPod(ctx, podName, node, corev1.PodResourceClaim{ResourceClaimName: ptr.To(claimName)})

		By("Verifying the claim remains unallocated and pod remains Pending")
		expectDRAPodUnschedulable(ctx, podName, claimName)
	})

	It("should not schedule a pod when DRA resource requests exceed available capacity", func(ctx SpecContext) {
		claimName := "test-dra-overcommit-claim"
		podName := "test-dra-overcommit-pod"

		DeferCleanup(func(cleanupCtx context.Context) {
			deletePodSync(cleanupCtx, namespaces.Test, podName)
			deleteClaimSync(cleanupCtx, namespaces.Test, claimName)
		})

		By("Creating a ResourceClaim requesting more devices than available on the node")
		createDRAClaim(ctx, claimName, deviceClassName, int64(updatedVFs+1), "")

		By("Creating a pod referencing the overcommitted ResourceClaim")
		createDRAPod(ctx, podName, node, corev1.PodResourceClaim{ResourceClaimName: ptr.To(claimName)})

		By("Verifying the claim remains unallocated and pod remains Pending")
		expectDRAPodUnschedulable(ctx, podName, claimName)
	})

	It("should allocate multiple SR-IOV devices to a single pod via ResourceClaim", func(ctx SpecContext) {
		claimName := "test-dra-multi-device-claim"
		podName := "test-dra-multi-device-pod"

		DeferCleanup(func(cleanupCtx context.Context) {
			deletePodSync(cleanupCtx, namespaces.Test, podName)
			deleteClaimSync(cleanupCtx, namespaces.Test, claimName)
		})

		By("Creating a ResourceClaim requesting 2 devices for DeviceClass " + deviceClassName)
		createDRAClaim(ctx, claimName, deviceClassName, 2, "")

		By("Creating a pod consuming the multi-device ResourceClaim")
		createdPod := createDRAPod(ctx, podName, node, corev1.PodResourceClaim{ResourceClaimName: ptr.To(claimName)})

		By("Waiting for the ResourceClaim to allocate 2 distinct devices")
		waitForDRAClaimAllocated(ctx, claimName, 2)

		By("Verifying the ResourceClaim is reserved for the pod")
		waitForDRAClaimReservedFor(ctx, claimName, createdPod.UID)

		By("Waiting for the pod to be Running on " + node)
		runningPod := waitForPodRunningOnNode(ctx, podName, node)

		By("Verifying both allocated devices are exposed to the pod")
		expectDRADevicesInPod(ctx, runningPod, claimName, extendedResource)
	})

	It("should enforce capacity limits across multiple pods sharing the resource pool", func(ctx SpecContext) {
		claimName1 := "test-dra-shared-claim-1"
		claimName2 := "test-dra-shared-claim-2"
		claimName3 := "test-dra-shared-claim-3"
		podName1 := "test-dra-shared-pod-1"
		podName2 := "test-dra-shared-pod-2"
		podName3 := "test-dra-shared-pod-3"

		DeferCleanup(func(cleanupCtx context.Context) {
			deletePodSync(cleanupCtx, namespaces.Test, podName3)
			deletePodSync(cleanupCtx, namespaces.Test, podName2)
			deletePodSync(cleanupCtx, namespaces.Test, podName1)
			deleteClaimSync(cleanupCtx, namespaces.Test, claimName3)
			deleteClaimSync(cleanupCtx, namespaces.Test, claimName2)
			deleteClaimSync(cleanupCtx, namespaces.Test, claimName1)
		})

		// The first two pods split the whole pool, leaving nothing for the third one.
		firstCount := updatedVFs / 2
		secondCount := updatedVFs - firstCount

		By(fmt.Sprintf("Creating first pod claiming %d devices", firstCount))
		createDRAClaim(ctx, claimName1, deviceClassName, int64(firstCount), "")
		createDRAPod(ctx, podName1, node, corev1.PodResourceClaim{ResourceClaimName: ptr.To(claimName1)})
		waitForPodRunningOnNode(ctx, podName1, node)

		By(fmt.Sprintf("Creating second pod claiming %d devices (exhausting all %d node VFs)", secondCount, updatedVFs))
		createDRAClaim(ctx, claimName2, deviceClassName, int64(secondCount), "")
		createDRAPod(ctx, podName2, node, corev1.PodResourceClaim{ResourceClaimName: ptr.To(claimName2)})
		waitForPodRunningOnNode(ctx, podName2, node)

		By("Creating third pod requesting 1 device when no capacity is left")
		createDRAClaim(ctx, claimName3, deviceClassName, 1, "")
		createDRAPod(ctx, podName3, node, corev1.PodResourceClaim{ResourceClaimName: ptr.To(claimName3)})
		expectDRAPodUnschedulable(ctx, podName3, claimName3)

		// Keep the first claim: its devices must be released by the pod going away.
		By("Deleting first pod to free up capacity for the pending third pod")
		deletePodSync(ctx, namespaces.Test, podName1)

		By("Verifying third pod is now allocated and transitions to Running")
		waitForDRAClaimAllocated(ctx, claimName3, 1)
		waitForPodRunningOnNode(ctx, podName3, node)
	})

	// Note: End-to-end pod network attachment with data-plane verification (traffic/ping on net1)
	// will be added once Multus CNI DRA support (k8snetworkplumbingwg/multus-cni#1455) is available in CI.

	It("should clean up DeviceAttributes and DeviceClass when a policy is deleted while DRA remains active", func(ctx SpecContext) {
		const secondPolicyVFs = 2
		var secondNode string
		var secondNic *sriovv1.InterfaceExt
		for _, nName := range sriovInfos.Nodes {
			devs, err := sriovInfos.FindSriovDevices(nName)
			Expect(err).ToNot(HaveOccurred(), "finding SR-IOV devices on node %s", nName)
			for _, d := range devs {
				if d.TotalVfs < secondPolicyVFs {
					continue
				}
				if nName != node || d.PciAddress != nic.PciAddress {
					secondNode = nName
					secondNic = d
					break
				}
			}
			if secondNic != nil {
				break
			}
		}
		if secondNic == nil {
			Skip(fmt.Sprintf("Test requires a second SR-IOV device supporting at least %d VFs", secondPolicyVFs))
		}

		secondResource := "seconddraresource"
		secondDCName := sriovv1.ResourceNameToDeviceClassName(secondResource)
		secondAttrsName := secondDCName + "-attrs"

		By("Creating secondary policy for resource " + secondResource + " on " + secondNode)
		created, err := network.CreateSriovPolicy(clients, "test-dra-second-policy-", operatorNamespace, secondNic.Name, secondNode, secondPolicyVFs, secondResource, "netdevice")
		Expect(err).ToNot(HaveOccurred())
		secondPolicyName := created.Name

		DeferCleanup(func(cleanupCtx context.Context) {
			deletePolicySync(cleanupCtx, secondPolicyName)
			WaitForSRIOVStable()
		})

		WaitForSRIOVStable()

		By("Waiting for secondary DeviceAttributes " + secondAttrsName + " to be created")
		Eventually(ctx, func(g Gomega) {
			attrs := &sriovdrav1alpha1.DeviceAttributes{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      secondAttrsName,
			}, attrs)
			g.Expect(err).ToNot(HaveOccurred())
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for secondary DeviceClass " + secondDCName + " to be created")
		Eventually(ctx, func(g Gomega) {
			dc := &resourceapi.DeviceClass{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{Name: secondDCName}, dc)
			g.Expect(err).ToNot(HaveOccurred())
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Deleting secondary policy " + secondPolicyName + " while DRA is still active")
		deletePolicySync(ctx, secondPolicyName)
		WaitForSRIOVStable()

		By("Waiting for secondary DeviceAttributes " + secondAttrsName + " to be removed")
		Eventually(ctx, func(g Gomega) {
			attrs := &sriovdrav1alpha1.DeviceAttributes{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      secondAttrsName,
			}, attrs)
			g.Expect(errors.IsNotFound(err)).To(BeTrue(), "DeviceAttributes %s still exists", secondAttrsName)
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for secondary DeviceClass " + secondDCName + " to be removed")
		Eventually(ctx, func(g Gomega) {
			dc := &resourceapi.DeviceClass{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{Name: secondDCName}, dc)
			g.Expect(errors.IsNotFound(err)).To(BeTrue(), "DeviceClass %s still exists", secondDCName)
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Verifying primary policy's DeviceClass " + deviceClassName + " is still present")
		primaryDC := &resourceapi.DeviceClass{}
		Expect(clients.Get(ctx, runtimeclient.ObjectKey{Name: deviceClassName}, primaryDC)).To(Succeed())

		By("Verifying primary policy's DeviceAttributes " + deviceAttrsName + " is still present")
		primaryAttrs := &sriovdrav1alpha1.DeviceAttributes{}
		Expect(clients.Get(ctx, runtimeclient.ObjectKey{
			Namespace: operatorNamespace,
			Name:      deviceAttrsName,
		}, primaryAttrs)).To(Succeed())
	})

	It("should expose VFIO device nodes to an unprivileged extended-resource consumer", func(ctx SpecContext) {
		testDRAVFIOExtendedResource(ctx, policyName)
	})

	It("should drain a ResourceClaim consumer when its SriovNetworkNodePolicy is deleted", func(ctx SpecContext) {
		disableDrain, err := cluster.GetNodeDrainState(clients, operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		if disableDrain {
			Skip("Drain is disabled in SriovOperatorConfig")
		}

		// The preceding VFIO spec may have selected a different node and device.
		// Use the current policy so this also works when that spec was skipped.
		policy := &sriovv1.SriovNetworkNodePolicy{}
		Expect(clients.Get(ctx, runtimeclient.ObjectKey{Namespace: operatorNamespace, Name: policyName}, policy)).To(Succeed())

		By("Creating an explicit ResourceClaim for the policy's DeviceClass")
		claim := &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "dra-drain-", Namespace: namespaces.Test},
			Spec: resourceapi.ResourceClaimSpec{
				Devices: resourceapi.DeviceClaim{
					Requests: []resourceapi.DeviceRequest{{
						Name: "vf",
						Exactly: &resourceapi.ExactDeviceRequest{
							DeviceClassName: deviceClassName,
							AllocationMode:  resourceapi.DeviceAllocationModeExactCount,
							Count:           1,
						},
					}},
				},
			},
		}
		Expect(clients.Create(ctx, claim)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(runtimeclient.IgnoreNotFound(clients.Delete(ctx, claim))).To(Succeed())
			Eventually(ctx, func() bool {
				return errors.IsNotFound(clients.Get(ctx, runtimeclient.ObjectKeyFromObject(claim), &resourceapi.ResourceClaim{}))
			}).WithTimeout(waitingTime).WithPolling(time.Second).Should(BeTrue())
		})

		By("Starting a pod that consumes the claim without extended-resource requests")
		consumer := pod.GetDefinition()
		consumer.Spec.NodeSelector = policy.Spec.NodeSelector
		consumer.Spec.ResourceClaims = []corev1.PodResourceClaim{{
			Name: "vf", ResourceClaimName: ptr.To(claim.Name),
		}}
		consumer.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{{Name: "vf"}}
		Expect(clients.Create(ctx, consumer)).To(Succeed())
		// Cleanup runs after the eviction assertion, and also handles a failed drain.
		// Register it after claim cleanup so the pod is removed first.
		DeferCleanup(func(ctx SpecContext) {
			Expect(runtimeclient.IgnoreNotFound(clients.Delete(ctx, consumer))).To(Succeed())
			Eventually(ctx, func() bool {
				return errors.IsNotFound(clients.Get(ctx, runtimeclient.ObjectKeyFromObject(consumer), &corev1.Pod{}))
			}).WithTimeout(waitingTime).WithPolling(time.Second).Should(BeTrue())
		})
		Eventually(ctx, func(g Gomega) {
			g.Expect(clients.Get(ctx, runtimeclient.ObjectKeyFromObject(consumer), consumer)).To(Succeed())
			g.Expect(pod.AssertContainerReady(*consumer, consumer.Spec.Containers[0].Name)).To(Succeed())
			g.Expect(consumer.Spec.NodeName).NotTo(BeEmpty())
			g.Expect(clients.Get(ctx, runtimeclient.ObjectKeyFromObject(claim), claim)).To(Succeed())
			g.Expect(claim.Status.Allocation).NotTo(BeNil())
			g.Expect(claim.Status.Allocation.Devices.Results).To(HaveLen(1))
			g.Expect(claim.Status.Allocation.Devices.Results[0].Driver).To(Equal(consts.DRADriverBaseDeviceClassName))
			g.Expect(claim.Status.ReservedFor).To(ContainElement(HaveField("UID", consumer.UID)))
		}).WithTimeout(waitingTime).WithPolling(time.Second).Should(Succeed())
		for _, container := range consumer.Spec.Containers {
			Expect(container.Resources.Requests).NotTo(HaveKey(corev1.ResourceName(extendedResource)))
			Expect(container.Resources.Limits).NotTo(HaveKey(corev1.ResourceName(extendedResource)))
		}

		By("Deleting the policy while the claim-backed pod is running")
		Expect(clients.Delete(ctx, policy)).To(Succeed())

		By("Waiting for the operator to drain the consumer before test cleanup")
		Eventually(ctx, func(g Gomega) {
			err := clients.Get(ctx, runtimeclient.ObjectKeyFromObject(consumer), &corev1.Pod{})
			g.Expect(errors.IsNotFound(err)).To(BeTrue(),
				"pod %s/%s on node %s was not drained after policy deletion (get error: %v)",
				consumer.Namespace, consumer.Name, consumer.Spec.NodeName, err)
		}).WithTimeout(waitingTime).WithPolling(time.Second).Should(Succeed())

		By("Waiting for node reconfiguration and withdrawal of the deleted policy's devices")
		WaitForSRIOVStable()
		Eventually(ctx, func(g Gomega) {
			count, err := countAdvertisedDRADevices(ctx, consumer.Spec.NodeName, extendedResource)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(count).To(BeZero())
		}).WithTimeout(waitingTime).WithPolling(time.Second).Should(Succeed())
	})

	It("should clean up DRA objects and restore the device plugin when DRA is disabled", func(ctx SpecContext) {
		By("Removing the DRA policy and waiting for node reconfiguration before switching providers")
		policy := &sriovv1.SriovNetworkNodePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: policyName, Namespace: operatorNamespace},
		}
		Expect(runtimeclient.IgnoreNotFound(clients.Delete(ctx, policy))).To(Succeed())
		WaitForSRIOVStable()

		By("Disabling dynamicResourceAllocation feature flag")
		setFeatureFlag(consts.DynamicResourceAllocationFeatureGate, false)
		WaitForSRIOVStable()

		By("Waiting for sriov-dra-driver DaemonSet to be removed")
		assertObjectIsNotFound(consts.DRADriverDaemonSetName, &appsv1.DaemonSet{})

		By("Waiting for operator-generated DeviceAttributes to be removed")
		Eventually(ctx, func(g Gomega) {
			list := &sriovdrav1alpha1.DeviceAttributesList{}
			err := clients.List(ctx, list,
				runtimeclient.InNamespace(operatorNamespace),
				runtimeclient.MatchingLabels(consts.DRAOperatorLabels))
			if meta.IsNoMatchError(err) {
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(list.Items).To(BeEmpty())
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for operator-generated SriovResourcePolicy to be removed")
		Eventually(ctx, func(g Gomega) {
			list := &sriovdrav1alpha1.SriovResourcePolicyList{}
			err := clients.List(ctx, list,
				runtimeclient.InNamespace(operatorNamespace),
				runtimeclient.MatchingLabels(consts.DRAOperatorLabels))
			if meta.IsNoMatchError(err) {
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(list.Items).To(BeEmpty())
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for operator-generated DeviceClasses to be removed")
		Eventually(ctx, func(g Gomega) {
			list := &resourceapi.DeviceClassList{}
			err := clients.List(ctx, list,
				runtimeclient.MatchingLabels(consts.DRAOperatorLabels))
			if meta.IsNoMatchError(err) {
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(list.Items).To(BeEmpty())
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for base DeviceClass to be removed")
		Eventually(ctx, func(g Gomega) {
			dc := &resourceapi.DeviceClass{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Name: consts.DRADriverBaseDeviceClassName,
			}, dc)
			g.Expect(errors.IsNotFound(err) || meta.IsNoMatchError(err)).To(BeTrue())
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		waitForDRAProviderSwitch(ctx, false)

		By("Waiting for the DRA driver's ResourceSlices to be withdrawn")
		Eventually(ctx, func(g Gomega) {
			list := &resourceapi.ResourceSliceList{}
			g.Expect(clients.List(ctx, list)).To(Succeed())
			for i := range list.Items {
				g.Expect(list.Items[i].Spec.Driver).ToNot(Equal(consts.DRADriverBaseDeviceClassName),
					"ResourceSlice %s is still published", list.Items[i].Name)
			}
		}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())

		By("Recreating the policy in device-plugin mode")
		_, err := network.CreateSriovPolicy(clients, "test-device-plugin-policy-", operatorNamespace, nic.Name, node, initialVFs, draResourceName, "netdevice")
		Expect(err).ToNot(HaveOccurred())
		WaitForSRIOVStable()

		By("Waiting for sriov-device-plugin DaemonSet to be restored")
		Eventually(ctx, func(g Gomega) {
			ds := &appsv1.DaemonSet{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      devicePluginDSName,
			}, ds)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ds.Status.NumberReady).To(Equal(ds.Status.DesiredNumberScheduled))
			g.Expect(ds.Status.DesiredNumberScheduled).To(BeNumerically(">", 0))
		}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for at least one Running device-plugin pod")
		Eventually(ctx, func(g Gomega) {
			pods, err := clients.Pods(operatorNamespace).List(ctx, metav1.ListOptions{
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
		}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for device-plugin-config ConfigMap to be restored")
		Eventually(ctx, func(g Gomega) {
			cfg := &corev1.ConfigMap{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{
				Namespace: operatorNamespace,
				Name:      consts.ConfigMapName,
			}, cfg)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(cfg.Data).To(HaveKeyWithValue(node, ContainSubstring(draResourceName)))
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("Waiting for node " + node + " to advertise allocatable " + extendedResource)
		Eventually(ctx, func(g Gomega) {
			n := &corev1.Node{}
			err := clients.Get(ctx, runtimeclient.ObjectKey{Name: node}, n)
			g.Expect(err).ToNot(HaveOccurred())
			qty, exists := n.Status.Allocatable[corev1.ResourceName(extendedResource)]
			g.Expect(exists).To(BeTrue(), "Node %s does not advertise %s", node, extendedResource)
			g.Expect(qty.Value()).To(BeNumerically("==", initialVFs))
		}).WithTimeout(waitingTime).WithPolling(5 * time.Second).Should(Succeed())
	})
})

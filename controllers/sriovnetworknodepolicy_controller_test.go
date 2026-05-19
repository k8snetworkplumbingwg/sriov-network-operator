package controllers

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/google/go-cmp/cmp"
	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	dptypes "github.com/k8snetworkplumbingwg/sriov-network-device-plugin/pkg/types"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func mustMarshallSelector(t *testing.T, input *dptypes.NetDeviceSelectors) *json.RawMessage {
	out, err := json.Marshal(input)
	if err != nil {
		t.Error(err)
		t.FailNow()
		return nil
	}
	ret := json.RawMessage(out)
	return &ret
}

func mustUnmarshallSelector(input *json.RawMessage) *dptypes.NetDeviceSelectors {
	ret := dptypes.NetDeviceSelectors{}
	err := json.Unmarshal(*input, &ret)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return &ret
}

func TestResolvePfNames(t *testing.T) {
	testCases := []struct {
		name           string
		pfNames        []string
		nodeState      *sriovnetworkv1.SriovNetworkNodeState
		expectedResult []string
	}{
		{
			name:           "empty pfNames returns empty slice",
			pfNames:        []string{},
			nodeState:      &sriovnetworkv1.SriovNetworkNodeState{},
			expectedResult: []string{},
		},
		{
			name:           "nil nodeState returns pfNames as-is",
			pfNames:        []string{"eth0", "eth1"},
			nodeState:      nil,
			expectedResult: []string{"eth0", "eth1"},
		},
		{
			name:    "pfNames with actual interface names",
			pfNames: []string{"ens803f0", "ens803f1"},
			nodeState: &sriovnetworkv1.SriovNetworkNodeState{
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: []sriovnetworkv1.InterfaceExt{
						{Name: "ens803f0", AltNames: []string{"alt1", "alt2"}},
						{Name: "ens803f1", AltNames: []string{"alt3"}},
					},
				},
			},
			expectedResult: []string{"ens803f0", "ens803f1"},
		},
		{
			name:    "pfNames with alternative names resolve to actual names",
			pfNames: []string{"alt1", "alt3"},
			nodeState: &sriovnetworkv1.SriovNetworkNodeState{
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: []sriovnetworkv1.InterfaceExt{
						{Name: "ens803f0", AltNames: []string{"alt1", "alt2"}},
						{Name: "ens803f1", AltNames: []string{"alt3"}},
					},
				},
			},
			expectedResult: []string{"ens803f0", "ens803f1"},
		},
		{
			name:    "mixed pfNames with actual and alternative names",
			pfNames: []string{"ens803f0", "alt3", "alt2"},
			nodeState: &sriovnetworkv1.SriovNetworkNodeState{
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: []sriovnetworkv1.InterfaceExt{
						{Name: "ens803f0", AltNames: []string{"alt1", "alt2"}},
						{Name: "ens803f1", AltNames: []string{"alt3"}},
					},
				},
			},
			expectedResult: []string{"ens803f0", "ens803f1", "ens803f0"},
		},
		{
			name:    "pfName not found in nodeState returns unchanged",
			pfNames: []string{"notfound", "ens803f0"},
			nodeState: &sriovnetworkv1.SriovNetworkNodeState{
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: []sriovnetworkv1.InterfaceExt{
						{Name: "ens803f0", AltNames: []string{"alt1"}},
					},
				},
			},
			expectedResult: []string{"notfound", "ens803f0"},
		},
		{
			name:    "pfNames with ranges preserved",
			pfNames: []string{"ens803f0#0-9", "alt1#10-19"},
			nodeState: &sriovnetworkv1.SriovNetworkNodeState{
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: []sriovnetworkv1.InterfaceExt{
						{Name: "ens803f0", AltNames: []string{"alt1"}},
					},
				},
			},
			expectedResult: []string{"ens803f0#0-9", "ens803f0#10-19"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := resolvePfNames(tc.pfNames, tc.nodeState)
			if len(result) != len(tc.expectedResult) {
				t.Errorf("resolvePfNames() returned slice of length %d, want %d", len(result), len(tc.expectedResult))
				return
			}
			for i := range result {
				if result[i] != tc.expectedResult[i] {
					t.Errorf("resolvePfNames()[%d] = %q, want %q", i, result[i], tc.expectedResult[i])
				}
			}
		})
	}
}

func TestRenderDevicePluginConfigData(t *testing.T) {
	table := []struct {
		tname       string
		policy      sriovnetworkv1.SriovNetworkNodePolicy
		expResource dptypes.ResourceConfList
	}{
		{
			tname: "testVirtioVdpaVirtio",
			policy: sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "resourceName",
					DeviceType:   consts.DeviceTypeNetDevice,
					VdpaType:     consts.VdpaTypeVirtio,
				},
			},
			expResource: dptypes.ResourceConfList{
				ResourceList: []dptypes.ResourceConfig{
					{
						ResourceName: "resourceName",
						Selectors: mustMarshallSelector(t, &dptypes.NetDeviceSelectors{
							VdpaType: dptypes.VdpaType(consts.VdpaTypeVirtio),
						}),
					},
				},
			},
		}, {
			tname: "testVhostVdpaVirtio",
			policy: sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "resourceName",
					DeviceType:   consts.DeviceTypeNetDevice,
					VdpaType:     consts.VdpaTypeVhost,
				},
			},
			expResource: dptypes.ResourceConfList{
				ResourceList: []dptypes.ResourceConfig{
					{
						ResourceName: "resourceName",
						Selectors: mustMarshallSelector(t, &dptypes.NetDeviceSelectors{
							VdpaType: dptypes.VdpaType(consts.VdpaTypeVhost),
						}),
					},
				},
			},
		},
		{
			tname: "testExcludeTopology",
			policy: sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName:    "resourceName",
					ExcludeTopology: true,
				},
			},
			expResource: dptypes.ResourceConfList{
				ResourceList: []dptypes.ResourceConfig{
					{
						ResourceName:    "resourceName",
						Selectors:       mustMarshallSelector(t, &dptypes.NetDeviceSelectors{}),
						ExcludeTopology: true,
					},
				},
			},
		},
	}

	reconciler := SriovNetworkNodePolicyReconciler{
		FeatureGate: featuregate.New(),
	}

	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
	nodeState := sriovnetworkv1.SriovNetworkNodeState{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: vars.Namespace}}

	scheme := runtime.NewScheme()
	utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
	reconciler.Client = fake.NewClientBuilder().
		WithScheme(scheme).WithObjects(&nodeState).
		Build()

	for _, tc := range table {
		policyList := sriovnetworkv1.SriovNetworkNodePolicyList{Items: []sriovnetworkv1.SriovNetworkNodePolicy{tc.policy}}

		t.Run(tc.tname, func(t *testing.T) {
			resourceList, err := reconciler.renderDevicePluginConfigData(context.TODO(), &policyList, &node)
			if err != nil {
				t.Error(tc.tname, "renderDevicePluginConfigData has failed")
			}

			if !cmp.Equal(resourceList, tc.expResource) {
				t.Error(tc.tname, "ResourceConfList not as expected", cmp.Diff(resourceList, tc.expResource))
			}
		})
	}
}

// --- Phase 1: DRA pure helper unit tests ---

func TestResourceNameToDeviceClassName(t *testing.T) {
	testCases := []struct {
		name         string
		resourceName string
		expected     string
	}{
		{"empty returns sriov", "", "sriov"},
		{"underscores to dashes", "intel_nic", "intel-nic"},
		{"uppercase to lowercase", "IntelNic", "intelnic"},
		{"mixed", "My_Resource_01", "my-resource-01"},
		{"trailing hyphen trimmed", "a1-", "a1"},
		{"non-DNS chars stripped", "foo.bar", "foobar"},
		{"leading/trailing hyphens trimmed", "-x-", "x"},
		{"leading underscores become hyphens then trimmed", "_foo", "foo"},
		{"only underscores/dashes becomes sriov", "___", "sriov"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := resourceNameToDeviceClassName(tc.resourceName)
			if got != tc.expected {
				t.Errorf("resourceNameToDeviceClassName(%q) = %q, want %q", tc.resourceName, got, tc.expected)
			}
		})
	}
}

func TestBuildExtendedResourceName(t *testing.T) {
	defer func(prev string) { vars.ResourcePrefix = prev }(vars.ResourcePrefix)

	testCases := []struct {
		name         string
		prefix       string
		resourceName string
		expected     string
	}{
		{"empty prefix returns resourceName as-is", "", "intel_nic", "intel_nic"},
		{"with prefix", "openshift.io", "intel_nic", "openshift.io/intel_nic"},
		{"empty prefix empty name", "", "", ""},
		{"prefix with empty name", "p", "", "p/"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vars.ResourcePrefix = tc.prefix
			got := buildExtendedResourceName(tc.resourceName)
			if got != tc.expected {
				t.Errorf("buildExtendedResourceName(%q) with prefix %q = %q, want %q", tc.resourceName, tc.prefix, got, tc.expected)
			}
		})
	}
}

func TestBuildDeviceClassCEL(t *testing.T) {
	defer func(prev string) { vars.ResourcePrefix = prev }(vars.ResourcePrefix)

	vars.ResourcePrefix = ""
	got := buildDeviceClassCEL("intel_nic")
	expectContain := `device.driver == "sriovnetwork.k8snetworkplumbingwg.io"`
	if !strings.Contains(got, expectContain) {
		t.Errorf("buildDeviceClassCEL() must contain driver check: got %q", got)
	}
	expectAttr := `device.attributes["k8s.cni.cncf.io"].resourceName == "intel_nic"`
	if !strings.Contains(got, expectAttr) {
		t.Errorf("buildDeviceClassCEL() must contain resourceName check: got %q", got)
	}

	// Escaping: quote and backslash in resourceName
	vars.ResourcePrefix = "p"
	got2 := buildDeviceClassCEL(`a"b\c`)
	if !strings.Contains(got2, `\"`) {
		t.Errorf("buildDeviceClassCEL() should escape quotes in extended name")
	}
	if !strings.Contains(got2, `\\`) {
		t.Errorf("buildDeviceClassCEL() should escape backslash in extended name")
	}
}

func TestBuildDeviceAttributesCR(t *testing.T) {
	defer func(prev string) { vars.Namespace = prev }(vars.Namespace)
	defer func(prev string) { vars.ResourcePrefix = prev }(vars.ResourcePrefix)
	vars.Namespace = "sriov-network-operator"
	vars.ResourcePrefix = "openshift.io"

	cr := buildDeviceAttributesCR("intel-nic-attrs", "intel_nic")

	if cr.Name != "intel-nic-attrs" || cr.Namespace != vars.Namespace {
		t.Errorf("Name=%q Namespace=%q, want intel-nic-attrs / %s", cr.Name, cr.Namespace, vars.Namespace)
	}
	if cr.Labels["sriovnetwork.openshift.io/generated-by"] != "sriov-network-operator" ||
		cr.Labels["sriovnetwork.openshift.io/resource-pool"] != "intel-nic" {
		t.Errorf("unexpected labels: %v", cr.Labels)
	}
	key := resourceapi.QualifiedName("k8s.cni.cncf.io/resourceName")
	attr, ok := cr.Spec.Attributes[key]
	if !ok || attr.StringValue == nil || *attr.StringValue != "openshift.io/intel_nic" {
		t.Errorf("expected attribute with extended resource name openshift.io/intel_nic, got %v", cr.Spec.Attributes)
	}
}

func TestBuildPolicyConfig(t *testing.T) {
	poolLabel := "intel-nic"
	deviceIDPassthrough := "154c"
	deviceIDVf := "10de"
	pfNames := []string{"ens1f0"}
	rootDevices := []string{"0000:08:00.0"}

	testCases := []struct {
		name      string
		policy    *sriovnetworkv1.SriovNetworkNodePolicy
		nodeState *sriovnetworkv1.SriovNetworkNodeState
		check     func(t *testing.T, c *sriovdrav1alpha1.Config)
	}{
		{
			name: "vendor and deviceID and pfNames and rootDevices",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "intel_nic",
					NicSelector: sriovnetworkv1.SriovNetworkNicSelector{
						Vendor:      "8086",
						DeviceID:    deviceIDPassthrough,
						PfNames:     pfNames,
						RootDevices: rootDevices,
					},
					NumVfs: 0,
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if c.DeviceAttributesSelector == nil || c.DeviceAttributesSelector.MatchLabels["sriovnetwork.openshift.io/resource-pool"] != poolLabel {
					t.Errorf("DeviceAttributesSelector should match pool %q", poolLabel)
				}
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				f := c.ResourceFilters[0]
				if len(f.Vendors) != 1 || f.Vendors[0] != "8086" {
					t.Errorf("Vendors want [8086], got %v", f.Vendors)
				}
				if len(f.Devices) != 1 || f.Devices[0] != deviceIDPassthrough {
					t.Errorf("Devices want [%s], got %v", deviceIDPassthrough, f.Devices)
				}
				if !cmp.Equal(f.PfNames, pfNames) {
					t.Errorf("PfNames: %v", cmp.Diff(f.PfNames, pfNames))
				}
				if !cmp.Equal(f.PfPciAddresses, rootDevices) {
					t.Errorf("PfPciAddresses: %v", cmp.Diff(f.PfPciAddresses, rootDevices))
				}
			},
		},
		{
			name: "vfio driver",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "vfio_nic",
					DeviceType:   consts.DeviceTypeVfioPci,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{},
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				if len(c.ResourceFilters[0].Drivers) != 1 || c.ResourceFilters[0].Drivers[0] != "vfio-pci" {
					t.Errorf("Drivers want [vfio-pci], got %v", c.ResourceFilters[0].Drivers)
				}
			},
		},
		{
			name: "deviceID with NumVfs uses GetVfDeviceID",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic",
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{DeviceID: deviceIDVf},
					NumVfs:       4,
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				// GetVfDeviceID may return "" for unknown deviceID; then Devices is not set
				f := c.ResourceFilters[0]
				if len(f.Devices) > 0 && f.Devices[0] == deviceIDVf {
					t.Errorf("with NumVfs>0 expect VF device ID or empty, got %v", f.Devices)
				}
			},
		},
		{
			name: "PfNames resolved via nodeState altNames",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic",
					NicSelector: sriovnetworkv1.SriovNetworkNicSelector{
						PfNames: []string{"enp3s0np0"},
					},
					NumVfs: 4,
				},
			},
			nodeState: &sriovnetworkv1.SriovNetworkNodeState{
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: []sriovnetworkv1.InterfaceExt{
						{
							Name:     "ens1f0",
							AltNames: []string{"enp3s0np0"},
						},
					},
				},
			},
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				f := c.ResourceFilters[0]
				if len(f.PfNames) != 1 || f.PfNames[0] != "ens1f0" {
					t.Errorf("PfNames want [ens1f0], got %v", f.PfNames)
				}
			},
		},
		{
			name: "PfNames with VF range resolved via nodeState altNames",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic",
					NicSelector: sriovnetworkv1.SriovNetworkNicSelector{
						PfNames: []string{"enp3s0np0#2-5"},
					},
					NumVfs: 8,
				},
			},
			nodeState: &sriovnetworkv1.SriovNetworkNodeState{
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: []sriovnetworkv1.InterfaceExt{
						{
							Name:     "ens1f0",
							AltNames: []string{"enp3s0np0"},
						},
					},
				},
			},
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				f := c.ResourceFilters[0]
				if len(f.PfNames) != 1 || f.PfNames[0] != "ens1f0#2-5" {
					t.Errorf("PfNames want [ens1f0#2-5], got %v", f.PfNames)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := buildPolicyConfig(tc.policy, tc.nodeState)
			if err != nil {
				t.Fatalf("buildPolicyConfig: %v", err)
			}
			tc.check(t, cfg)
		})
	}
}

func TestBuildDeviceClassUnstructured(t *testing.T) {
	deviceClassName := "intel-nic"
	resourceName := "intel_nic"
	extendedResourceName := "openshift.io/intel_nic"
	celExpr := `device.driver == "sriovnetwork.k8snetworkplumbingwg.io" && device.attributes["k8s.cni.cncf.io"].resourceName == "openshift.io/intel_nic"`

	obj := buildDeviceClassUnstructured(deviceClassName, resourceName, extendedResourceName, celExpr)

	if obj.GetName() != deviceClassName {
		t.Errorf("GetName() = %q, want %q", obj.GetName(), deviceClassName)
	}
	if obj.GetObjectKind().GroupVersionKind().Kind != "DeviceClass" ||
		obj.GetObjectKind().GroupVersionKind().Group != "resource.k8s.io" {
		t.Errorf("GVK = %v", obj.GetObjectKind().GroupVersionKind())
	}
	if obj.GetLabels()["sriovnetwork.openshift.io/generated-by"] != "sriov-network-operator" {
		t.Errorf("generated-by label: %v", obj.GetLabels())
	}
	if obj.GetLabels()["sriovnetwork.openshift.io/resource-name"] != resourceName {
		t.Errorf("resource-name label: %v", obj.GetLabels())
	}
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if spec["extendedResourceName"] != extendedResourceName {
		t.Errorf("spec.extendedResourceName = %v", spec["extendedResourceName"])
	}
	selectors, _, _ := unstructured.NestedSlice(obj.Object, "spec", "selectors")
	if len(selectors) != 1 {
		t.Fatalf("spec.selectors length = %d", len(selectors))
	}
	sel, _ := selectors[0].(map[string]interface{})
	cel, _ := sel["cel"].(map[string]interface{})
	expr, _ := cel["expression"].(string)
	if expr != celExpr {
		t.Errorf("spec.selectors[0].cel.expression = %q, want %q", expr, celExpr)
	}
}

var _ = Describe("SriovnetworkNodePolicy controller", Ordered, func() {
	var cancel context.CancelFunc
	var ctx context.Context

	BeforeAll(func() {
		// disable stale state cleanup delay to check that the controller can cleanup state objects
		DeferCleanup(os.Setenv, "STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", os.Getenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES"))
		os.Setenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", "0")

		By("Create SriovOperatorConfig controller k8s objs")
		config := makeDefaultSriovOpConfig()
		Expect(k8sClient.Create(context.Background(), config)).Should(Succeed())
		DeferCleanup(func() {
			err := k8sClient.Delete(context.Background(), config)
			Expect(err).ToNot(HaveOccurred())
		})

		// setup controller manager
		By("Setup controller manager")
		k8sManager, err := setupK8sManagerForTest()
		Expect(err).ToNot(HaveOccurred())

		err = (&SriovNetworkNodePolicyReconciler{
			Client:      k8sManager.GetClient(),
			Scheme:      k8sManager.GetScheme(),
			FeatureGate: featuregate.New(),
		}).SetupWithManager(k8sManager)
		Expect(err).ToNot(HaveOccurred())

		ctx, cancel = context.WithCancel(context.Background())

		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer GinkgoRecover()
			By("Start controller manager")
			err := k8sManager.Start(ctx)
			Expect(err).ToNot(HaveOccurred())
		}()

		DeferCleanup(func() {
			By("Shut down manager")
			cancel()
			wg.Wait()
		})
	})
	AfterEach(func() {
		err := k8sClient.DeleteAllOf(context.Background(), &corev1.Node{}, k8sclient.GracePeriodSeconds(0))
		Expect(err).ToNot(HaveOccurred())

		err = k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodePolicy{}, k8sclient.InNamespace(vars.Namespace), k8sclient.GracePeriodSeconds(0))
		Expect(err).ToNot(HaveOccurred())

		err = k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodeState{}, k8sclient.InNamespace(vars.Namespace), k8sclient.GracePeriodSeconds(0))
		Expect(err).ToNot(HaveOccurred())
	})
	Context("device plugin labels", func() {
		It("Should add the right labels to the nodes", func() {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "node0",
				Labels: map[string]string{"kubernetes.io/os": "linux",
					"node-role.kubernetes.io/worker": ""},
			}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.TODO(), k8sclient.ObjectKey{Name: "node0", Namespace: testNamespace}, nodeState)
				g.Expect(err).ToNot(HaveOccurred())
			}, time.Minute, time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: node.Name}, node)
				g.Expect(err).ToNot(HaveOccurred())
				value, exist := node.Labels[consts.SriovDevicePluginLabel]
				g.Expect(exist).To(BeTrue())
				g.Expect(value).To(Equal(consts.SriovDevicePluginLabelDisabled))
			}, time.Minute, time.Second).Should(Succeed())

			nodeState.Status.Interfaces = sriovnetworkv1.InterfaceExts{
				sriovnetworkv1.InterfaceExt{
					Vendor:     "8086",
					Driver:     "i40e",
					Mtu:        1500,
					Name:       "ens803f0",
					PciAddress: "0000:86:00.0",
					NumVfs:     0,
					TotalVfs:   64,
				},
			}
			err := k8sClient.Status().Update(context.Background(), nodeState)
			Expect(err).ToNot(HaveOccurred())

			somePolicy := &sriovnetworkv1.SriovNetworkNodePolicy{}
			somePolicy.SetNamespace(testNamespace)
			somePolicy.SetName("some-policy")
			somePolicy.Spec = sriovnetworkv1.SriovNetworkNodePolicySpec{
				NumVfs:       5,
				NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
				NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				Priority:     20,
			}
			Expect(k8sClient.Create(context.Background(), somePolicy)).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: node.Name}, node)
				g.Expect(err).ToNot(HaveOccurred())
				value, exist := node.Labels[consts.SriovDevicePluginLabel]
				g.Expect(exist).To(BeTrue())
				g.Expect(value).To(Equal(consts.SriovDevicePluginLabelEnabled))
			}, time.Minute, time.Second).Should(Succeed())

			delete(node.Labels, "node-role.kubernetes.io/worker")
			err = k8sClient.Update(context.Background(), node)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: node.Name}, node)
				g.Expect(err).ToNot(HaveOccurred())
				_, exist := node.Labels[consts.SriovDevicePluginLabel]
				g.Expect(exist).To(BeFalse())
			}, time.Minute, time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: node.Name, Namespace: testNamespace}, nodeState)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("should skip label removal for nodes that doesn't exist with no stale timer", func() {
			node0 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "node0",
				Labels: map[string]string{"kubernetes.io/os": "linux",
					"node-role.kubernetes.io/worker": ""},
			}}
			Expect(k8sClient.Create(ctx, node0)).To(Succeed())

			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{"kubernetes.io/os": "linux",
					"node-role.kubernetes.io/worker": ""},
			}}
			Expect(k8sClient.Create(ctx, node1)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
			node := &corev1.Node{}
			for _, nodeName := range []string{"node0", "node1"} {
				Eventually(func(g Gomega) {
					err := k8sClient.Get(context.TODO(), k8sclient.ObjectKey{Name: nodeName, Namespace: testNamespace}, nodeState)
					g.Expect(err).ToNot(HaveOccurred())
				}, time.Minute, time.Second).Should(Succeed())

				Eventually(func(g Gomega) {
					err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: nodeName}, node)
					g.Expect(err).ToNot(HaveOccurred())
					value, exist := node.Labels[consts.SriovDevicePluginLabel]
					g.Expect(exist).To(BeTrue())
					g.Expect(value).To(Equal(consts.SriovDevicePluginLabelDisabled))
				}, time.Minute, time.Second).Should(Succeed())

				nodeState.Status.Interfaces = sriovnetworkv1.InterfaceExts{
					sriovnetworkv1.InterfaceExt{
						Vendor:     "8086",
						Driver:     "i40e",
						Mtu:        1500,
						Name:       "ens803f0",
						PciAddress: "0000:86:00.0",
						NumVfs:     0,
						TotalVfs:   64,
					},
				}
				err := k8sClient.Status().Update(context.Background(), nodeState)
				Expect(err).ToNot(HaveOccurred())
			}

			err := k8sClient.Delete(context.Background(), node1, k8sclient.GracePeriodSeconds(0))
			Expect(err).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.TODO(), k8sclient.ObjectKey{Name: "node1", Namespace: testNamespace}, nodeState)
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, 30*time.Second, time.Second).Should(Succeed())

			somePolicy := &sriovnetworkv1.SriovNetworkNodePolicy{}
			somePolicy.SetNamespace(testNamespace)
			somePolicy.SetName("some-policy")
			somePolicy.Spec = sriovnetworkv1.SriovNetworkNodePolicySpec{
				NumVfs:       5,
				NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
				NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				Priority:     20,
			}
			Expect(k8sClient.Create(context.Background(), somePolicy)).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: node0.Name}, node0)
				g.Expect(err).ToNot(HaveOccurred())
				value, exist := node0.Labels[consts.SriovDevicePluginLabel]
				g.Expect(exist).To(BeTrue())
				g.Expect(value).To(Equal(consts.SriovDevicePluginLabelEnabled))
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("should skip label removal for nodes that doesn't exist with stale timer", func() {
			err := os.Setenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", "5")
			Expect(err).ToNot(HaveOccurred())
			defer func() {
				err = os.Unsetenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES")
				Expect(err).ToNot(HaveOccurred())
			}()

			node0 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "node0",
				Labels: map[string]string{"kubernetes.io/os": "linux",
					"node-role.kubernetes.io/worker": ""},
			}}
			Expect(k8sClient.Create(ctx, node0)).To(Succeed())

			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{"kubernetes.io/os": "linux",
					"node-role.kubernetes.io/worker": ""},
			}}
			Expect(k8sClient.Create(ctx, node1)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
			node := &corev1.Node{}
			for _, nodeName := range []string{"node0", "node1"} {
				Eventually(func(g Gomega) {
					err := k8sClient.Get(context.TODO(), k8sclient.ObjectKey{Name: nodeName, Namespace: testNamespace}, nodeState)
					g.Expect(err).ToNot(HaveOccurred())
				}, time.Minute, time.Second).Should(Succeed())

				Eventually(func(g Gomega) {
					err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: nodeName}, node)
					g.Expect(err).ToNot(HaveOccurred())
					value, exist := node.Labels[consts.SriovDevicePluginLabel]
					g.Expect(exist).To(BeTrue())
					g.Expect(value).To(Equal(consts.SriovDevicePluginLabelDisabled))
				}, time.Minute, time.Second).Should(Succeed())

				nodeState.Status.Interfaces = sriovnetworkv1.InterfaceExts{
					sriovnetworkv1.InterfaceExt{
						Vendor:     "8086",
						Driver:     "i40e",
						Mtu:        1500,
						Name:       "ens803f0",
						PciAddress: "0000:86:00.0",
						NumVfs:     0,
						TotalVfs:   64,
					},
				}
				err := k8sClient.Status().Update(context.Background(), nodeState)
				Expect(err).ToNot(HaveOccurred())
			}

			err = k8sClient.Delete(context.Background(), node1, k8sclient.GracePeriodSeconds(0))
			Expect(err).ToNot(HaveOccurred())

			Consistently(func(g Gomega) {
				err := k8sClient.Get(context.TODO(), k8sclient.ObjectKey{Name: "node1", Namespace: testNamespace}, nodeState)
				g.Expect(err).ToNot(HaveOccurred())
			}, 10*time.Second, time.Second).Should(Succeed())

			somePolicy := &sriovnetworkv1.SriovNetworkNodePolicy{}
			somePolicy.SetNamespace(testNamespace)
			somePolicy.SetName("some-policy")
			somePolicy.Spec = sriovnetworkv1.SriovNetworkNodePolicySpec{
				NumVfs:       5,
				NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
				NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				Priority:     20,
			}
			Expect(k8sClient.Create(context.Background(), somePolicy)).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: node0.Name}, node0)
				g.Expect(err).ToNot(HaveOccurred())
				value, exist := node0.Labels[consts.SriovDevicePluginLabel]
				g.Expect(exist).To(BeTrue())
				g.Expect(value).To(Equal(consts.SriovDevicePluginLabelEnabled))
			}, time.Minute, time.Second).Should(Succeed())
		})
	})

	Context("RdmaMode", func() {
		BeforeEach(func() {
			Expect(
				k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkPoolConfig{}, k8sclient.InNamespace(vars.Namespace)),
			).ToNot(HaveOccurred())
		})

		It("field is correctly written to the SriovNetworkNodeState", func() {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "node0",
				Labels: map[string]string{
					"node-role.kubernetes.io/worker": "",
					"kubernetes.io/os":               "linux",
					"test":                           "",
				},
			}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.TODO(), k8sclient.ObjectKey{Name: "node0", Namespace: testNamespace}, nodeState)
				g.Expect(err).ToNot(HaveOccurred())
			}, time.Minute, time.Second).Should(Succeed())

			nodeState.Status.Interfaces = sriovnetworkv1.InterfaceExts{
				sriovnetworkv1.InterfaceExt{
					Vendor:     "8086",
					Driver:     "i40e",
					Mtu:        1500,
					Name:       "ens803f0",
					PciAddress: "0000:86:00.0",
					NumVfs:     0,
					TotalVfs:   64,
				},
			}
			err := k8sClient.Status().Update(context.Background(), nodeState)
			Expect(err).ToNot(HaveOccurred())

			poolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{}
			poolConfig.SetNamespace(testNamespace)
			poolConfig.SetName("test-workers")
			poolConfig.Spec = sriovnetworkv1.SriovNetworkPoolConfigSpec{
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "",
					},
				},
				RdmaMode: "exclusive",
			}
			Expect(k8sClient.Create(ctx, poolConfig)).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{Name: node.Name, Namespace: testNamespace}, nodeState)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(nodeState.Spec.System.RdmaMode).To(Equal("exclusive"))
			}).WithPolling(time.Second).WithTimeout(time.Minute).Should(Succeed())

		})
	})
})

var _ = Describe("SriovNetworkNodePolicyReconciler", Ordered, func() {
	Context("handleStaleNodeState", func() {
		var (
			ctx       context.Context
			r         *SriovNetworkNodePolicyReconciler
			nodeState *sriovnetworkv1.SriovNetworkNodeState
		)

		BeforeEach(func() {
			ctx = context.Background()
			scheme := runtime.NewScheme()
			utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
			nodeState = &sriovnetworkv1.SriovNetworkNodeState{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			r = &SriovNetworkNodePolicyReconciler{Client: fake.NewClientBuilder().WithObjects(nodeState).Build()}
		})
		It("should set default delay", func() {
			nodeState := nodeState.DeepCopy()
			Expect(r.handleStaleNodeState(ctx, nodeState)).NotTo(HaveOccurred())
			Expect(r.Get(ctx, types.NamespacedName{Name: nodeState.Name}, nodeState)).NotTo(HaveOccurred())
			Expect(time.Now().UTC().Before(nodeState.GetKeepUntilTime())).To(BeTrue())
		})
		It("should remove CR if wait time expired", func() {
			nodeState := nodeState.DeepCopy()
			nodeState.SetKeepUntilTime(time.Now().UTC().Add(-time.Minute))
			Expect(r.handleStaleNodeState(ctx, nodeState)).NotTo(HaveOccurred())
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Name: nodeState.Name}, nodeState))).To(BeTrue())
		})
		It("should keep existing wait time if already set", func() {
			nodeState := nodeState.DeepCopy()
			nodeState.SetKeepUntilTime(time.Now().UTC().Add(time.Minute))
			testTime := nodeState.GetKeepUntilTime()
			r.Update(ctx, nodeState)
			Expect(r.handleStaleNodeState(ctx, nodeState)).NotTo(HaveOccurred())
			Expect(r.Get(ctx, types.NamespacedName{Name: nodeState.Name}, nodeState)).NotTo(HaveOccurred())
			Expect(nodeState.GetKeepUntilTime()).To(Equal(testTime))
		})
		It("non default dealy", func() {
			DeferCleanup(os.Setenv, "STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", os.Getenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES"))
			os.Setenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", "60")
			nodeState := nodeState.DeepCopy()
			Expect(r.handleStaleNodeState(ctx, nodeState)).NotTo(HaveOccurred())
			Expect(r.Get(ctx, types.NamespacedName{Name: nodeState.Name}, nodeState)).NotTo(HaveOccurred())
			Expect(time.Until(nodeState.GetKeepUntilTime()) > 30*time.Minute).To(BeTrue())
		})
		It("invalid non default delay - should use default", func() {
			DeferCleanup(os.Setenv, "STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", os.Getenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES"))
			os.Setenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", "-20")
			nodeState := nodeState.DeepCopy()
			Expect(r.handleStaleNodeState(ctx, nodeState)).NotTo(HaveOccurred())
			Expect(r.Get(ctx, types.NamespacedName{Name: nodeState.Name}, nodeState)).NotTo(HaveOccurred())
			Expect(time.Until(nodeState.GetKeepUntilTime()) > 20*time.Minute).To(BeTrue())
		})
		It("should remove CR if delay is zero", func() {
			DeferCleanup(os.Setenv, "STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", os.Getenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES"))
			os.Setenv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES", "0")
			nodeState := nodeState.DeepCopy()
			Expect(r.handleStaleNodeState(ctx, nodeState)).NotTo(HaveOccurred())
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Name: nodeState.Name}, nodeState))).To(BeTrue())
		})
	})

	Context("renderDevicePluginConfigData", func() {
		It("should render device plugin config data when policies with the same resource name target different devices", func() {

			intelNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "intelNode", Labels: map[string]string{"node-role.kubernetes.io/worker": ""}}}
			mlxNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "mlxNode", Labels: map[string]string{"node-role.kubernetes.io/worker": ""}}}

			objs := []k8sclient.Object{
				intelNode,
				&sriovnetworkv1.SriovNetworkNodeState{ObjectMeta: metav1.ObjectMeta{Name: "intelNode", Namespace: testNamespace}, Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: sriovnetworkv1.InterfaceExts{
						{Driver: "ice", DeviceID: "159b", Vendor: "8086", PciAddress: "0000:31:00.0"},
					},
				}},
				mlxNode,
				&sriovnetworkv1.SriovNetworkNodeState{ObjectMeta: metav1.ObjectMeta{Name: "mlxNode", Namespace: testNamespace}, Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: sriovnetworkv1.InterfaceExts{
						{Driver: "mlx5_core", DeviceID: "101d", Vendor: "15b3", PciAddress: "0000:ca:00.0"},
					},
				}},
			}

			r := &SriovNetworkNodePolicyReconciler{Client: fake.NewClientBuilder().WithObjects(objs...).Build()}

			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "intel-vfio-pci"},
						Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
							ResourceName: "resource1",
							DeviceType:   "vfio-pci",
							NicSelector: sriovnetworkv1.SriovNetworkNicSelector{
								Vendor:      "8086",
								DeviceID:    "159b",
								RootDevices: []string{"0000:31:00.0"},
							},
							NumVfs:       128,
							NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "mellanox-rdma"},
						Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
							ResourceName: "resource1",
							DeviceType:   "netdevice",
							IsRdma:       true,
							NicSelector: sriovnetworkv1.SriovNetworkNicSelector{
								Vendor:      "15b3",
								DeviceID:    "101d",
								RootDevices: []string{"0000:ca:00.0"},
							},
							NumVfs:       128,
							NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
						},
					},
				},
			}
			rcl, err := r.renderDevicePluginConfigData(context.Background(), pl, mlxNode)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(rcl.ResourceList)).To(Equal(1))
			Expect(rcl.ResourceList[0].ResourceName).To(Equal("resource1"))
			selectors := mustUnmarshallSelector(rcl.ResourceList[0].Selectors)
			Expect(selectors.Vendors).To(ContainElements("8086", "15b3"))
			Expect(selectors.RootDevices).To(ContainElements("0000:31:00.0", "0000:ca:00.0"))

			// Having drivers in the selector cause the device plugin to fail to select the mellanox devices in the mlxNode
			Expect(selectors.Drivers).To(BeEmpty())
		})

		It("should render device plugin config data when policies configure vfio-pci and netdevice", func() {
			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"node-role.kubernetes.io/worker": ""}}}
			objs := []k8sclient.Object{
				node1,
				&sriovnetworkv1.SriovNetworkNodeState{ObjectMeta: metav1.ObjectMeta{Name: "node1", Namespace: testNamespace}, Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: sriovnetworkv1.InterfaceExts{
						{Driver: "ice", DeviceID: "159b", Vendor: "8086", PciAddress: "0000:31:00.0", Name: "ens0"},
					},
				}},
			}

			r := &SriovNetworkNodePolicyReconciler{Client: fake.NewClientBuilder().WithObjects(objs...).Build()}

			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "intel-vfio-pci"},
						Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
							ResourceName: "resvfiopci",
							DeviceType:   "vfio-pci",
							NicSelector: sriovnetworkv1.SriovNetworkNicSelector{
								Vendor:   "8086",
								DeviceID: "159b",
								PfNames:  []string{"ens0#0-9"},
							},
							NumVfs:       128,
							NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "mellanox-rdma"},
						Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
							ResourceName: "resnetdevice",
							DeviceType:   "netdevice",
							NicSelector: sriovnetworkv1.SriovNetworkNicSelector{
								Vendor:   "8086",
								DeviceID: "159b",
								PfNames:  []string{"ens0#10-19"},
							},
							NumVfs:       128,
							NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
						},
					},
				},
			}
			rcl, err := r.renderDevicePluginConfigData(context.Background(), pl, node1)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(rcl.ResourceList)).To(Equal(2))
			selectors := map[string]string{
				rcl.ResourceList[0].ResourceName: string(*rcl.ResourceList[0].Selectors),
				rcl.ResourceList[1].ResourceName: string(*rcl.ResourceList[1].Selectors),
			}
			Expect(selectors).To(HaveKeyWithValue("resvfiopci", `{"vendors":["8086"],"pfNames":["ens0#0-9"],"IsRdma":false,"NeedVhostNet":false}`))
			Expect(selectors).To(HaveKeyWithValue("resnetdevice", `{"vendors":["8086"],"pfNames":["ens0#10-19"],"IsRdma":false,"NeedVhostNet":false}`))
		})
	})

	// --- Phase 3: DRA sync and cleanup (syncDeviceAttributes, syncSriovResourcePolicies, syncExtendedResourceDeviceClasses, cleanup*) ---
	Context("DRA sync and cleanup", func() {
		var (
			ctx         context.Context
			scheme      *runtime.Scheme
			r           *SriovNetworkNodePolicyReconciler
			dc          *sriovnetworkv1.SriovOperatorConfig
			nsSaved     string
			prefixSaved string
		)

		beforeEachDRA := func(objs ...k8sclient.Object) {
			ctx = context.Background()
			nsSaved = vars.Namespace
			prefixSaved = vars.ResourcePrefix
			vars.Namespace = testNamespace
			vars.ResourcePrefix = "openshift.io"
			DeferCleanup(func() {
				vars.Namespace = nsSaved
				vars.ResourcePrefix = prefixSaved
			})
			scheme = runtime.NewScheme()
			utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
			utilruntime.Must(sriovdrav1alpha1.AddToScheme(scheme))
			utilruntime.Must(corev1.AddToScheme(scheme))
			utilruntime.Must(resourceapi.AddToScheme(scheme))
			dc = &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: testNamespace},
			}
			allObjs := append([]k8sclient.Object{dc}, objs...)
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(allObjs...).Build()
			fg := featuregate.New()
			fg.Init(map[string]bool{consts.DynamicResourceAllocationFeatureGate: true})
			r = &SriovNetworkNodePolicyReconciler{Client: client, Scheme: scheme, FeatureGate: fg}
		}

		It("syncDeviceAttributes creates DeviceAttributes for each policy resource name", func() {
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			beforeEachDRA()
			Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
			attr := &sriovdrav1alpha1.DeviceAttributes{}
			Expect(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "intel-nic-attrs"}, attr)).To(Succeed())
			Expect(attr.Labels["sriovnetwork.openshift.io/resource-pool"]).To(Equal("intel-nic"))
			key := resourceapi.QualifiedName("k8s.cni.cncf.io/resourceName")
			Expect(attr.Spec.Attributes).To(HaveKey(key))
			Expect(attr.Spec.Attributes[key].StringValue).NotTo(BeNil())
			Expect(*attr.Spec.Attributes[key].StringValue).To(Equal("openshift.io/intel_nic"))
		})

		It("syncDeviceAttributes removes DeviceAttributes when resource name no longer in policies", func() {
			attr := &sriovdrav1alpha1.DeviceAttributes{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "old-attrs",
					Namespace: testNamespace,
					Labels: map[string]string{
						"sriovnetwork.openshift.io/generated-by":  "sriov-network-operator",
						"sriovnetwork.openshift.io/resource-pool": "old",
					},
				},
				Spec: sriovdrav1alpha1.DeviceAttributesSpec{Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}},
			}
			beforeEachDRA(attr)
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{Items: []sriovnetworkv1.SriovNetworkNodePolicy{}}
			Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
			err := r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "old-attrs"}, attr)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("syncSriovResourcePolicies creates SriovResourcePolicy per node", func() {
			nodeName := "worker-0"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						"node-role.kubernetes.io/worker": "",
						"kubernetes.io/hostname":         nodeName,
					},
				},
			}
			nodeState := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName, Namespace: testNamespace},
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: sriovnetworkv1.InterfaceExts{
						{Vendor: "8086", Driver: "i40e", PciAddress: "0000:86:00.0"},
					},
				},
			}
			beforeEachDRA(node, nodeState)
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: testNamespace},
						Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
							ResourceName: "intel_nic",
							NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
							NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
						},
					},
				},
			}
			nl := &corev1.NodeList{Items: []corev1.Node{*node}}
			Expect(r.syncSriovResourcePolicies(ctx, dc, pl, nl)).To(Succeed())
			policy := &sriovdrav1alpha1.SriovResourcePolicy{}
			Expect(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: nodeName}, policy)).To(Succeed())
			Expect(policy.Spec.NodeSelector).NotTo(BeNil())
			Expect(policy.Spec.NodeSelector.NodeSelectorTerms).To(HaveLen(1))
			reqs := policy.Spec.NodeSelector.NodeSelectorTerms[0].MatchExpressions
			Expect(reqs).To(HaveLen(1))
			Expect(reqs[0].Key).To(Equal(corev1.LabelHostname))
			Expect(reqs[0].Operator).To(Equal(corev1.NodeSelectorOpIn))
			Expect(reqs[0].Values).To(Equal([]string{nodeName}))
			Expect(policy.Spec.Configs).To(HaveLen(1))
			Expect(policy.Spec.Configs[0].DeviceAttributesSelector).NotTo(BeNil())
			Expect(policy.Spec.Configs[0].DeviceAttributesSelector.MatchLabels).To(HaveKeyWithValue("sriovnetwork.openshift.io/resource-pool", "intel-nic"))
			Expect(policy.Spec.Configs[0].ResourceFilters).To(HaveLen(1))
			Expect(policy.Spec.Configs[0].ResourceFilters[0].Vendors).To(Equal([]string{"8086"}))
		})

		It("syncExtendedResourceDeviceClasses creates DeviceClass per resource name", func() {
			beforeEachDRA()
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
			Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(Succeed())
			dcList := &unstructured.UnstructuredList{}
			dcList.SetGroupVersionKind(schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceClassList"})
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels{"sriovnetwork.openshift.io/generated-by": "sriov-network-operator"})).To(Succeed())
			Expect(dcList.Items).To(HaveLen(1))
			Expect(dcList.Items[0].GetName()).To(Equal("intel-nic"))
			extName, _, _ := unstructured.NestedString(dcList.Items[0].Object, "spec", "extendedResourceName")
			Expect(extName).To(Equal("openshift.io/intel_nic"))
		})

		It("cleanupExtendedResourceDeviceClasses deletes operator-created DeviceClasses", func() {
			deviceClass := &unstructured.Unstructured{}
			deviceClass.SetGroupVersionKind(schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceClass"})
			deviceClass.SetName("intel-nic")
			deviceClass.SetLabels(map[string]string{"sriovnetwork.openshift.io/generated-by": "sriov-network-operator"})
			beforeEachDRA(deviceClass)
			Expect(r.cleanupExtendedResourceDeviceClasses(ctx)).To(Succeed())
			dcList := &unstructured.UnstructuredList{}
			dcList.SetGroupVersionKind(schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceClassList"})
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels{"sriovnetwork.openshift.io/generated-by": "sriov-network-operator"})).To(Succeed())
			Expect(dcList.Items).To(BeEmpty())
		})

		It("syncExtendedResourceDeviceClasses skips gracefully when DeviceClass CRD is not available", func() {
			ctx = context.Background()
			nsSaved = vars.Namespace
			prefixSaved = vars.ResourcePrefix
			vars.Namespace = testNamespace
			vars.ResourcePrefix = "openshift.io"
			DeferCleanup(func() {
				vars.Namespace = nsSaved
				vars.ResourcePrefix = prefixSaved
			})
			noDeviceClassScheme := runtime.NewScheme()
			utilruntime.Must(sriovnetworkv1.AddToScheme(noDeviceClassScheme))
			utilruntime.Must(sriovdrav1alpha1.AddToScheme(noDeviceClassScheme))
			utilruntime.Must(corev1.AddToScheme(noDeviceClassScheme))
			dc = &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: testNamespace},
			}
			cl := fake.NewClientBuilder().WithScheme(noDeviceClassScheme).WithObjects(dc).Build()
			fg := featuregate.New()
			fg.Init(map[string]bool{consts.DynamicResourceAllocationFeatureGate: true})
			r = &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: noDeviceClassScheme, FeatureGate: fg}
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(Succeed())
		})

		It("cleanupExtendedResourceDeviceClasses skips gracefully when DeviceClass CRD is not available", func() {
			ctx = context.Background()
			nsSaved = vars.Namespace
			prefixSaved = vars.ResourcePrefix
			vars.Namespace = testNamespace
			vars.ResourcePrefix = "openshift.io"
			DeferCleanup(func() {
				vars.Namespace = nsSaved
				vars.ResourcePrefix = prefixSaved
			})
			noDeviceClassScheme := runtime.NewScheme()
			utilruntime.Must(sriovnetworkv1.AddToScheme(noDeviceClassScheme))
			utilruntime.Must(sriovdrav1alpha1.AddToScheme(noDeviceClassScheme))
			utilruntime.Must(corev1.AddToScheme(noDeviceClassScheme))
			cl := fake.NewClientBuilder().WithScheme(noDeviceClassScheme).Build()
			fg := featuregate.New()
			fg.Init(map[string]bool{consts.DynamicResourceAllocationFeatureGate: true})
			r = &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: noDeviceClassScheme, FeatureGate: fg}
			Expect(r.cleanupExtendedResourceDeviceClasses(ctx)).To(Succeed())
		})

		It("cleanupSriovResourcePoliciesAndDeviceAttributes deletes operator-created policies and attributes", func() {
			policy := &sriovdrav1alpha1.SriovResourcePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: testNamespace,
					Labels:    map[string]string{"sriovnetwork.openshift.io/generated-by": "sriov-network-operator"},
				},
			}
			attr := &sriovdrav1alpha1.DeviceAttributes{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "intel-nic-attrs",
					Namespace: testNamespace,
					Labels:    map[string]string{"sriovnetwork.openshift.io/generated-by": "sriov-network-operator"},
				},
			}
			beforeEachDRA(policy, attr)
			Expect(r.cleanupSriovResourcePoliciesAndDeviceAttributes(ctx)).To(Succeed())
			var gotPolicy sriovdrav1alpha1.SriovResourcePolicy
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "foo"}, &gotPolicy))).To(BeTrue())
			var gotAttr sriovdrav1alpha1.DeviceAttributes
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "intel-nic-attrs"}, &gotAttr))).To(BeTrue())
		})
	})
})

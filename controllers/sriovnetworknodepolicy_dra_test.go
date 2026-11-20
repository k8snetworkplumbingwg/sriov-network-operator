package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/google/go-cmp/cmp"
	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	configapi "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/virtualfunction/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	drapkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/dra"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// --- Phase 1: DRA pure helper unit tests ---

func TestNodeHostname(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-0.example.com",
			Labels: map[string]string{
				corev1.LabelHostname: "worker-0",
			},
		},
	}
	got, err := nodeHostname(node)
	if err != nil {
		t.Fatalf("nodeHostname() unexpected error: %v", err)
	}
	if got != "worker-0" {
		t.Errorf("nodeHostname() = %q, want %q", got, "worker-0")
	}

	node.Labels = nil
	_, err = nodeHostname(node)
	if err == nil {
		t.Fatal("nodeHostname() without label should return an error")
	}
}

func TestCollectDRAPolicyResources(t *testing.T) {
	pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
		Items: []sriovnetworkv1.SriovNetworkNodePolicy{
			{ObjectMeta: metav1.ObjectMeta{Name: "p1"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "p3"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "mlx5"}},
		},
	}
	got, err := drapkg.PolicyResourceDrivers(pl.Items)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("PolicyResourceDrivers() returned %d names, want 2", len(got))
	}
	if _, ok := got["intel_nic"]; !ok {
		t.Error("expected intel_nic in result")
	}
	if _, ok := got["mlx5"]; !ok {
		t.Error("expected mlx5 in result")
	}
}

func TestValidatedDRAPolicyResources(t *testing.T) {
	pl := &sriovnetworkv1.SriovNetworkNodePolicyList{Items: []sriovnetworkv1.SriovNetworkNodePolicy{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "INTEL_NIC"}},
	}}
	resources, err := validatedDRAPolicyResources(pl)
	g := NewWithT(t)
	g.Expect(err).To(MatchError(ContainSubstring("normalizes to the same DRA device class name")))
	g.Expect(resources).To(BeNil())
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
	expectContain := fmt.Sprintf(`device.driver == "%s"`, consts.DRADriverBaseDeviceClassName)
	if !strings.Contains(got, expectContain) {
		t.Errorf("buildDeviceClassCEL() must contain driver check: got %q", got)
	}
	attrNS, attrName, _ := strings.Cut(drapkg.ResourceNameAttributeKey, "/")
	expectAttr := fmt.Sprintf(`device.attributes["%s"].%s == "intel_nic"`, attrNS, attrName)
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

func TestDeviceClassCELMatchesDevices(t *testing.T) {
	defer func(prev string) { vars.ResourcePrefix = prev }(vars.ResourcePrefix)
	vars.ResourcePrefix = "openshift.io"
	compiled := dracel.GetCompiler(dracel.Features{}).CompileCELExpression(buildDeviceClassCEL("intel_nic"), dracel.Options{})
	if compiled.Error != nil {
		t.Fatal(compiled.Error)
	}
	for _, tc := range []struct {
		name, driver, attribute, value string
		want                           bool
	}{
		{"matching resource", consts.DRADriverBaseDeviceClassName, drapkg.ResourceNameAttributeKey, "openshift.io/intel_nic", true},
		{"different resource", consts.DRADriverBaseDeviceClassName, drapkg.ResourceNameAttributeKey, "openshift.io/other", false},
		{"different driver", "other.example.com", drapkg.ResourceNameAttributeKey, "openshift.io/intel_nic", false},
		{"missing attribute namespace", consts.DRADriverBaseDeviceClassName, "", "", false},
		{"missing resourceName", consts.DRADriverBaseDeviceClassName, "k8s.cni.cncf.io/other", "value", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := dracel.Device{Driver: tc.driver}
			if tc.attribute != "" {
				device.Attributes = map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					resourceapi.QualifiedName(tc.attribute): {StringValue: &tc.value},
				}
			}
			got, _, err := compiled.DeviceMatches(context.Background(), device)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("device matches = %t, want %t", got, tc.want)
			}
		})
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
	if cr.Labels[drapkg.GeneratedByLabel] != consts.SriovNetworkOperatorIdentifier ||
		cr.Labels[drapkg.ResourcePoolLabel] != "intel-nic" {
		t.Errorf("unexpected labels: %v", cr.Labels)
	}
	key := resourceapi.QualifiedName(drapkg.ResourceNameAttributeKey)
	attr, ok := cr.Spec.Attributes[key]
	if !ok || attr.StringValue == nil || *attr.StringValue != "openshift.io/intel_nic" {
		t.Errorf("expected attribute with extended resource name openshift.io/intel_nic, got %v", cr.Spec.Attributes)
	}
}

func TestBuildPolicyConfig(t *testing.T) {
	oldNicIDMap := sriovnetworkv1.NicIDMap
	sriovnetworkv1.NicIDMap = []string{"8086 1572 154c"}
	t.Cleanup(func() { sriovnetworkv1.NicIDMap = oldNicIDMap })
	poolLabel := "intel-nic"
	deviceIDPassthrough := "154c"
	pfDeviceID := "1572"
	pfNames := []string{"ens1f0"}
	rootDevices := []string{"0000:08:00.0"}

	testCases := []struct {
		name      string
		policy    *sriovnetworkv1.SriovNetworkNodePolicy
		nodeState *sriovnetworkv1.SriovNetworkNodeState
		wantErr   bool
		wantSkip  bool
		check     func(t *testing.T, c *sriovdrav1alpha1.Config)
	}{
		{
			name:    "nil policy",
			policy:  nil,
			wantErr: true,
		},
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
				if c.DeviceAttributesSelector == nil || c.DeviceAttributesSelector.MatchLabels[drapkg.ResourcePoolLabel] != poolLabel {
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
			name: "netFilter nic selector is skipped",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "openstack_nic",
					NicSelector: sriovnetworkv1.SriovNetworkNicSelector{
						NetFilter: "openstack/NetworkID:ada9ec67-2c97-467c-b674-c47200e2f5da",
					},
				},
			},
			nodeState: nil,
			wantSkip:  true,
		},
		{
			name: "empty nic selector omits ResourceFilter",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "intel_nic",
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{},
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if c.DeviceAttributesSelector == nil || c.DeviceAttributesSelector.MatchLabels[drapkg.ResourcePoolLabel] != poolLabel {
					t.Errorf("DeviceAttributesSelector should match pool %q", poolLabel)
				}
				if len(c.ResourceFilters) != 0 {
					t.Errorf("empty nic selector should omit ResourceFilter, got %+v", c.ResourceFilters)
				}
			},
		},
		{
			name: "deviceID with NumVfs uses GetVfDeviceID",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic",
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{DeviceID: pfDeviceID},
					NumVfs:       4,
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				f := c.ResourceFilters[0]
				if !cmp.Equal(f.Devices, []string{"154c"}) {
					t.Errorf("Devices want [154c], got %v", f.Devices)
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
			name: "linkType eth",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic",
					LinkType:     consts.LinkTypeETH,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{},
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				want := strings.ToLower(consts.LinkTypeETH)
				if c.ResourceFilters[0].LinkType != want {
					t.Errorf("LinkType want %q, got %q", want, c.ResourceFilters[0].LinkType)
				}
			},
		},
		{
			name: "linkType ib",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic",
					LinkType:     consts.LinkTypeIB,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{},
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				want := strings.ToLower(consts.LinkTypeIB)
				if c.ResourceFilters[0].LinkType != want {
					t.Errorf("LinkType want %q, got %q", want, c.ResourceFilters[0].LinkType)
				}
			},
		},
		{
			name: "linkType omitted for vfio-pci",
			policy: &sriovnetworkv1.SriovNetworkNodePolicy{
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "vfio_nic",
					DeviceType:   consts.DeviceTypeVfioPci,
					LinkType:     consts.LinkTypeETH,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{},
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter, got %d", len(c.ResourceFilters))
				}
				f := c.ResourceFilters[0]
				if f.LinkType != "" {
					t.Errorf("LinkType want empty for vfio-pci, got %q", f.LinkType)
				}
				if len(f.Drivers) != 1 || f.Drivers[0] != "vfio-pci" {
					t.Errorf("Drivers want [vfio-pci], got %v", f.Drivers)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, skip, err := buildPolicyConfig(tc.policy, tc.nodeState)
			if tc.wantSkip {
				if !skip || cfg != nil || err == nil {
					t.Fatalf("buildPolicyConfig: expected skip with reason error, got skip=%v cfg=%v err=%v", skip, cfg, err)
				}
				return
			}
			if tc.wantErr {
				if err == nil || skip || cfg != nil {
					t.Fatalf("buildPolicyConfig: expected error, got skip=%v err=%v", skip, err)
				}
				return
			}
			if err != nil || skip {
				t.Fatalf("buildPolicyConfig: skip=%v err=%v", skip, err)
			}
			tc.check(t, cfg)
		})
	}
}

func TestBuildPolicyConfigRejectsUnmappedDeviceID(t *testing.T) {
	oldNicIDMap := sriovnetworkv1.NicIDMap
	sriovnetworkv1.NicIDMap = []string{"8086 1572 154c"}
	t.Cleanup(func() { sriovnetworkv1.NicIDMap = oldNicIDMap })
	for _, vendor := range []string{"", "8086"} {
		t.Run("vendor="+vendor, func(t *testing.T) {
			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "unknown-model"},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic", NumVfs: 4,
					NicSelector: sriovnetworkv1.SriovNetworkNicSelector{Vendor: vendor, DeviceID: "ffff"},
				},
			}
			cfg, skip, err := buildPolicyConfig(policy, nil)
			if err == nil || skip || cfg != nil {
				t.Fatalf("expected configuration error without a broadened filter, got config=%+v skip=%t err=%v", cfg, skip, err)
			}
			if !strings.Contains(err.Error(), policy.Name) || !strings.Contains(err.Error(), "ffff") {
				t.Errorf("error should identify the policy and unmapped device ID: %v", err)
			}
		})
	}
}

func TestRenderSriovResourcePolicyPreservesNodeStatePrecedence(t *testing.T) {
	oldNamespace := vars.Namespace
	vars.Namespace = testNamespace
	t.Cleanup(func() { vars.Namespace = oldNamespace })

	for _, tc := range []struct {
		name              string
		genericName       string
		dedicatedName     string
		genericPriority   int
		dedicatedPriority int
		wantResource      string
	}{
		{
			name:        "numeric priority overrides name order",
			genericName: "z-generic", dedicatedName: "a-dedicated",
			genericPriority: 99, dedicatedPriority: 10, wantResource: "dedicated",
		},
		{
			name:        "equal priority with dedicated policy last",
			genericName: "a-generic", dedicatedName: "z-dedicated",
			genericPriority: 10, dedicatedPriority: 10, wantResource: "dedicated",
		},
		{
			name:        "equal priority with generic policy last",
			genericName: "z-generic", dedicatedName: "a-dedicated",
			genericPriority: 10, dedicatedPriority: 10, wantResource: "generic",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "worker-0", Labels: map[string]string{corev1.LabelHostname: "worker-0"},
			}}
			state := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: vars.Namespace},
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
					Interfaces: sriovnetworkv1.InterfaceExts{
						{Name: "ens1f0", PciAddress: "0000:86:00.0", Vendor: "8086", TotalVfs: 8},
						{Name: "ens1f1", PciAddress: "0000:86:00.1", Vendor: "8086", TotalVfs: 8},
					},
				},
			}
			policies := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: tc.genericName},
						Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
							Priority: tc.genericPriority, ResourceName: "generic", NumVfs: 4,
							DeviceType:   consts.DeviceTypeNetDevice,
							NodeSelector: node.Labels,
							NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: tc.dedicatedName},
						Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
							Priority: tc.dedicatedPriority, ResourceName: "dedicated", NumVfs: 4,
							DeviceType:   consts.DeviceTypeNetDevice,
							NodeSelector: node.Labels,
							NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{PfNames: []string{"ens1f0"}},
						},
					},
				},
			}
			// Reconcile sorts once and passes the same order to both renderers.
			sort.Sort(sriovnetworkv1.ByPriority(policies.Items))
			g.Expect(renderNodePolicies(state, policies, node, nil)).To(Succeed())
			g.Expect(state.Spec.Interfaces).To(HaveLen(2))
			for _, iface := range state.Spec.Interfaces {
				wantResource := "generic"
				if iface.PciAddress == "0000:86:00.0" {
					wantResource = tc.wantResource
				}
				g.Expect(iface.VfGroups).To(HaveLen(1))
				g.Expect(iface.VfGroups[0].ResourceName).To(Equal(wantResource))
			}

			scheme := runtime.NewScheme()
			g.Expect(sriovnetworkv1.AddToScheme(scheme)).To(Succeed())
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(state).Build()
			r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme}
			policy, err := r.renderSriovResourcePolicyForNode(context.Background(), policies, node,
				map[string]struct{}{"generic": {}, "dedicated": {}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(policy).NotTo(BeNil())

			// Both filters match ens1f0's VFs: the driver's first match must agree
			// with NodeState. Only the vendor filter matches ens1f1's VFs.
			wantConfigs := []sriovdrav1alpha1.Config{
				{
					DeviceAttributesSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{drapkg.ResourcePoolLabel: "dedicated"},
					},
					ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{PfNames: []string{"ens1f0"}}},
				},
				{
					DeviceAttributesSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{drapkg.ResourcePoolLabel: "generic"},
					},
					ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"8086"}}},
				},
			}
			if tc.wantResource == "generic" {
				wantConfigs[0], wantConfigs[1] = wantConfigs[1], wantConfigs[0]
			}
			g.Expect(policy.Spec.Configs).To(Equal(wantConfigs))
		})
	}
}

func TestDeviceClassAPIUnavailable(t *testing.T) {
	list := &resourceapi.DeviceClassList{}
	scheme := runtime.NewScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	err := cl.List(context.Background(), list)
	if err == nil {
		t.Fatal("expected list error without DeviceClass in scheme")
	}
	if !deviceClassAPIUnavailable(err) {
		t.Errorf("deviceClassAPIUnavailable() = false, want true for %v", err)
	}
}

func TestBuildExtendedResourceDeviceClass(t *testing.T) {
	deviceClassName := "intel-nic"
	resourceName := "intel_nic"
	extendedResourceName := "openshift.io/intel_nic"
	celExpr := `device.driver == "sriovnetwork.k8snetworkplumbingwg.io" && device.attributes["k8s.cni.cncf.io"].resourceName == "openshift.io/intel_nic"`

	obj, err := buildExtendedResourceDeviceClass(deviceClassName, resourceName, extendedResourceName, celExpr, "")
	if err != nil {
		t.Fatal(err)
	}

	if obj.Name != deviceClassName {
		t.Errorf("Name = %q, want %q", obj.Name, deviceClassName)
	}
	if obj.Labels[drapkg.GeneratedByLabel] != consts.SriovNetworkOperatorIdentifier {
		t.Errorf("generated-by label: %v", obj.Labels)
	}
	if obj.Labels[drapkg.DeviceClassResourceNameLabel] != resourceName {
		t.Errorf("resource-name label: %v", obj.Labels)
	}
	if obj.Spec.ExtendedResourceName == nil || *obj.Spec.ExtendedResourceName != extendedResourceName {
		t.Errorf("spec.extendedResourceName = %v", obj.Spec.ExtendedResourceName)
	}
	if len(obj.Spec.Selectors) != 1 || obj.Spec.Selectors[0].CEL == nil {
		t.Fatalf("spec.selectors = %v", obj.Spec.Selectors)
	}
	if obj.Spec.Selectors[0].CEL.Expression != celExpr {
		t.Errorf("spec.selectors[0].cel.expression = %q, want %q", obj.Spec.Selectors[0].CEL.Expression, celExpr)
	}
}

func TestSyncDRARejectsMixedResourceDrivers(t *testing.T) {
	r := &SriovNetworkNodePolicyReconciler{}
	pl := &sriovnetworkv1.SriovNetworkNodePolicyList{Items: []sriovnetworkv1.SriovNetworkNodePolicy{
		{ObjectMeta: metav1.ObjectMeta{Name: "vfio"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "shared", DeviceType: consts.DeviceTypeVfioPci}},
		{ObjectMeta: metav1.ObjectMeta{Name: "netdevice"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "shared", DeviceType: consts.DeviceTypeNetDevice}},
	}}
	ctx := context.Background()
	// All three sync paths must reject conflicting policies before touching the
	// API, including when admission was bypassed or policies already existed.
	for name, sync := range map[string]func() error{
		"classes":           func() error { return r.syncExtendedResourceDeviceClasses(ctx, nil, pl) },
		"attributes":        func() error { return r.syncDeviceAttributes(ctx, nil, pl) },
		"resource policies": func() error { return r.syncSriovResourcePolicies(ctx, nil, pl, &corev1.NodeList{}) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := sync(); err == nil || !strings.Contains(err.Error(), "incompatible deviceType") {
				t.Fatalf("expected conflicting device types, got %v", err)
			}
		})
	}
}

func TestSyncExtendedResourceDeviceClassesVFIOConfiguration(t *testing.T) {
	ctx := context.Background()
	previousPrefix := vars.ResourcePrefix
	vars.ResourcePrefix = "openshift.io"
	t.Cleanup(func() { vars.ResourcePrefix = previousPrefix })
	scheme := runtime.NewScheme()
	if err := resourceapi.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &SriovNetworkNodePolicyReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	pl := &sriovnetworkv1.SriovNetworkNodePolicyList{Items: []sriovnetworkv1.SriovNetworkNodePolicy{
		{ObjectMeta: metav1.ObjectMeta{Name: "vfio"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
			ResourceName: "dpdk_vfio", DeviceType: consts.DeviceTypeVfioPci,
		}},
	}}
	for step, deviceType := range []string{consts.DeviceTypeVfioPci, consts.DeviceTypeVfioPci, consts.DeviceTypeNetDevice, consts.DeviceTypeVfioPci} {
		pl.Items[0].Spec.DeviceType = deviceType
		if err := r.syncExtendedResourceDeviceClasses(ctx, nil, pl); err != nil {
			t.Fatal(err)
		}
		got := &resourceapi.DeviceClass{}
		if err := r.Get(ctx, k8sclient.ObjectKey{Name: "dpdk-vfio"}, got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.ExtendedResourceName == nil || *got.Spec.ExtendedResourceName != "openshift.io/dpdk_vfio" {
			t.Fatalf("unexpected extended resource name: %v", got.Spec.ExtendedResourceName)
		}
		version := got.ResourceVersion
		if err := r.syncExtendedResourceDeviceClasses(ctx, nil, pl); err != nil {
			t.Fatal(err)
		}
		if err := r.Get(ctx, k8sclient.ObjectKeyFromObject(got), got); err != nil {
			t.Fatal(err)
		}
		if got.ResourceVersion != version {
			t.Fatal("unchanged configuration caused a redundant DeviceClass update")
		}
		if deviceType == consts.DeviceTypeNetDevice {
			if len(got.Spec.Config) != 0 {
				t.Fatalf("netdevice class retained VFIO configuration: %+v", got.Spec.Config)
			}
			continue
		}
		if len(got.Spec.Config) != 1 || got.Spec.Config[0].Opaque == nil {
			t.Fatalf("VFIO class must supply one opaque configuration, got %+v", got.Spec.Config)
		}
		opaque := got.Spec.Config[0].Opaque
		if opaque.Driver != consts.DRADriverBaseDeviceClassName {
			t.Fatalf("unexpected DRA driver: %s", opaque.Driver)
		}
		// Decode the serialized parameters with the pinned driver's real decoder.
		raw, err := json.Marshal(opaque.Parameters)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := runtime.Decode(configapi.Decoder, raw)
		if err != nil {
			t.Fatal(err)
		}
		config, ok := decoded.(*configapi.VfConfig)
		if !ok || config.Driver != consts.DeviceTypeVfioPci {
			t.Fatalf("unexpected VFIO configuration: %+v", decoded)
		}
		// Simulate configuration drift; the next sync must restore it.
		if step == 0 {
			got.Spec.Config = nil
			if err := r.Update(ctx, got); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestExtendedDeviceClassHasOwnershipMarker(t *testing.T) {
	dc := &resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{drapkg.DeviceClassResourceNameLabel: "intel_nic"},
	}}
	if !extendedDeviceClassHasOwnershipMarker(dc, "intel_nic") {
		t.Fatal("expected ownership marker for matching resource name")
	}
	if extendedDeviceClassHasOwnershipMarker(dc, "other") {
		t.Fatal("expected no ownership marker for mismatched resource name")
	}
	if extendedDeviceClassHasOwnershipMarker(&resourceapi.DeviceClass{}, "intel_nic") {
		t.Fatal("expected no ownership marker without labels")
	}
}

func TestSyncExtendedResourceDeviceClassesForeignNameConflict(t *testing.T) {
	ctx := context.Background()
	nsSaved := vars.Namespace
	prefixSaved := vars.ResourcePrefix
	vars.Namespace = testNamespace
	vars.ResourcePrefix = "openshift.io"
	defer func() {
		vars.Namespace = nsSaved
		vars.ResourcePrefix = prefixSaved
	}()

	scheme := runtime.NewScheme()
	utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
	utilruntime.Must(sriovdrav1alpha1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(resourceapi.AddToScheme(scheme))
	dc := &sriovnetworkv1.SriovOperatorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: testNamespace},
	}
	foreign := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "intel-nic"},
		Spec: resourceapi.DeviceClassSpec{
			Selectors: []resourceapi.DeviceSelector{
				{CEL: &resourceapi.CELDeviceSelector{Expression: `device.driver == "other"`}},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dc, foreign).Build()
	fg := featuregate.New()
	fg.Init(map[string]bool{consts.DynamicResourceAllocationFeatureGate: true})
	r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme, FeatureGate: fg}
	pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
		Items: []sriovnetworkv1.SriovNetworkNodePolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNamespace},
				Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
			},
		},
	}
	err := r.syncExtendedResourceDeviceClasses(ctx, dc, pl)
	if err == nil {
		t.Fatal("expected conflict when DeviceClass name collides without ownership marker")
	}
	if !errors.IsConflict(err) {
		t.Fatalf("expected conflict error, got %v", err)
	}
	got := &resourceapi.DeviceClass{}
	if err := cl.Get(ctx, k8sclient.ObjectKey{Name: "intel-nic"}, got); err != nil {
		t.Fatalf("Get foreign DeviceClass: %v", err)
	}
	if got.Spec.Selectors[0].CEL.Expression != `device.driver == "other"` {
		t.Fatalf("foreign DeviceClass spec was modified: %q", got.Spec.Selectors[0].CEL.Expression)
	}
}

var _ = Describe("SriovNetworkNodePolicyReconciler DRA", Ordered, func() {
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
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: testNamespace, UID: "operator-owner"},
			}
			allObjs := append([]k8sclient.Object{dc}, objs...)
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(allObjs...).Build()
			fg := featuregate.New()
			fg.Init(map[string]bool{consts.DynamicResourceAllocationFeatureGate: true})
			r = &SriovNetworkNodePolicyReconciler{Client: client, Scheme: scheme, FeatureGate: fg}
		}

		It("DRA sync and cleanup are idempotent across repeated reconcile", func() {
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

			attrList := &sriovdrav1alpha1.DeviceAttributesList{}
			var attrVersion string
			for i := range 3 {
				Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
				Expect(r.List(ctx, attrList, k8sclient.InNamespace(testNamespace),
					k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
				Expect(attrList.Items).To(HaveLen(1))
				if i == 0 {
					attrVersion = attrList.Items[0].ResourceVersion
					Expect(attrVersion).NotTo(BeEmpty())
				} else {
					Expect(attrList.Items[0].ResourceVersion).To(Equal(attrVersion), "repeated sync must not update DeviceAttributes")
				}
			}

			policyList := &sriovdrav1alpha1.SriovResourcePolicyList{}
			var policyVersion string
			for i := range 3 {
				Expect(r.syncSriovResourcePolicies(ctx, dc, pl, nl)).To(Succeed())
				Expect(r.List(ctx, policyList, k8sclient.InNamespace(testNamespace),
					k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
				Expect(policyList.Items).To(HaveLen(1))
				if i == 0 {
					policyVersion = policyList.Items[0].ResourceVersion
					Expect(policyVersion).NotTo(BeEmpty())
				} else {
					Expect(policyList.Items[0].ResourceVersion).To(Equal(policyVersion), "repeated sync must not update SriovResourcePolicy")
				}
			}

			dcList := &resourceapi.DeviceClassList{}
			var classVersion string
			for i := range 3 {
				Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(Succeed())
				Expect(r.List(ctx, dcList, k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
				Expect(dcList.Items).To(HaveLen(1))
				if i == 0 {
					classVersion = dcList.Items[0].ResourceVersion
					Expect(classVersion).NotTo(BeEmpty())
				} else {
					Expect(dcList.Items[0].ResourceVersion).To(Equal(classVersion), "repeated sync must not update DeviceClass")
				}
			}

			for range 3 {
				Expect(r.cleanupSriovResourcePoliciesAndDeviceAttributes(ctx, dc)).To(Succeed())
				Expect(cleanupExtendedResourceDeviceClasses(ctx, r.Client)).To(Succeed())
			}
			Expect(r.List(ctx, attrList, k8sclient.InNamespace(testNamespace),
				k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(attrList.Items).To(BeEmpty())
			Expect(r.List(ctx, policyList, k8sclient.InNamespace(testNamespace),
				k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(policyList.Items).To(BeEmpty())
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(dcList.Items).To(BeEmpty())
		})

		It("syncDeviceAttributes adopts CR when operator managed label was removed", func() {
			attr := buildDeviceAttributesCR("intel-nic-attrs", "intel_nic")
			attr.Labels = map[string]string{drapkg.ResourcePoolLabel: "intel-nic"}
			attr.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(
				&sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, UID: "operator-owner"}},
				sriovnetworkv1.SchemeGroupVersion.WithKind("SriovOperatorConfig"))}
			beforeEachDRA(attr)
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
			got := &sriovdrav1alpha1.DeviceAttributes{}
			Expect(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "intel-nic-attrs"}, got)).To(Succeed())
			Expect(got.Labels[drapkg.GeneratedByLabel]).To(Equal(consts.SriovNetworkOperatorIdentifier))
			Expect(got.Labels[drapkg.ResourcePoolLabel]).To(Equal("intel-nic"))
			Expect(metav1.IsControlledBy(got, dc)).To(BeTrue(), "adopted DeviceAttributes should reference SriovOperatorConfig")
		})

		It("syncDeviceAttributes restores controller owner reference when labels and spec already match", func() {
			beforeEachDRA()
			attr := buildDeviceAttributesCR("intel-nic-attrs", "intel_nic")
			attr.OwnerReferences = nil
			Expect(r.Create(ctx, attr)).To(Succeed())
			desired := buildDeviceAttributesCR("intel-nic-attrs", "intel_nic")
			Expect(controllerutil.SetControllerReference(dc, desired, scheme)).To(Succeed())
			Expect(attr.Namespace).To(Equal(desired.Namespace))
			Expect(attr.Spec).To(Equal(desired.Spec))
			Expect(attr.Labels).To(Equal(desired.Labels))
			Expect(attr.OwnerReferences).To(BeEmpty())
			Expect(desired.OwnerReferences).NotTo(BeEmpty())
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
			got := &sriovdrav1alpha1.DeviceAttributes{}
			Expect(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "intel-nic-attrs"}, got)).To(Succeed())
			Expect(metav1.IsControlledBy(got, dc)).To(BeTrue())
			Expect(got.Spec).To(Equal(attr.Spec))
			Expect(got.Labels).To(Equal(attr.Labels))
		})

		It("syncSriovResourcePolicies adopts CR when operator managed label was removed", func() {
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
			existingPolicy := &sriovdrav1alpha1.SriovResourcePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeName,
					Namespace: testNamespace,
					Labels:    map[string]string{drapkg.SriovResourcePolicyNodeLabel: nodeName},
				},
				Spec: sriovdrav1alpha1.SriovResourcePolicySpec{},
			}
			existingPolicy.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(
				&sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, UID: "operator-owner"}},
				sriovnetworkv1.SchemeGroupVersion.WithKind("SriovOperatorConfig"))}
			beforeEachDRA(node, nodeState, existingPolicy)
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
			got := &sriovdrav1alpha1.SriovResourcePolicy{}
			Expect(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: nodeName}, got)).To(Succeed())
			Expect(got.Labels[drapkg.GeneratedByLabel]).To(Equal(consts.SriovNetworkOperatorIdentifier))
			Expect(got.Spec.Configs).To(HaveLen(1))
			Expect(metav1.IsControlledBy(got, dc)).To(BeTrue(), "adopted SriovResourcePolicy should reference SriovOperatorConfig")
		})

		It("syncExtendedResourceDeviceClasses returns conflict when DeviceClass name collides without ownership marker", func() {
			foreign := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{Name: "intel-nic"},
				Spec: resourceapi.DeviceClassSpec{
					Selectors: []resourceapi.DeviceSelector{
						{CEL: &resourceapi.CELDeviceSelector{Expression: `device.driver == "other"`}},
					},
				},
			}
			beforeEachDRA(foreign)
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			err := r.syncExtendedResourceDeviceClasses(ctx, dc, pl)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsConflict(err)).To(BeTrue())
			got := &resourceapi.DeviceClass{}
			Expect(r.Get(ctx, k8sclient.ObjectKey{Name: "intel-nic"}, got)).To(Succeed())
			Expect(got.Spec.Selectors[0].CEL.Expression).To(Equal(`device.driver == "other"`))
		})

		It("syncExtendedResourceDeviceClasses adopts CR when operator managed label was removed", func() {
			obj, err := buildExtendedResourceDeviceClass("intel-nic", "intel_nic", "openshift.io/intel_nic", buildDeviceClassCEL("intel_nic"), "")
			Expect(err).NotTo(HaveOccurred())
			obj.Labels = map[string]string{drapkg.DeviceClassResourceNameLabel: "intel_nic"}
			beforeEachDRA(obj)
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(Succeed())
			got := &resourceapi.DeviceClass{}
			Expect(r.Get(ctx, k8sclient.ObjectKey{Name: "intel-nic"}, got)).To(Succeed())
			Expect(got.Labels[drapkg.GeneratedByLabel]).To(Equal(consts.SriovNetworkOperatorIdentifier))
		})

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
			Expect(attr.Labels[drapkg.ResourcePoolLabel]).To(Equal("intel-nic"))
			key := resourceapi.QualifiedName(drapkg.ResourceNameAttributeKey)
			Expect(attr.Spec.Attributes).To(HaveKey(key))
			Expect(attr.Spec.Attributes[key].StringValue).NotTo(BeNil())
			Expect(*attr.Spec.Attributes[key].StringValue).To(Equal("openshift.io/intel_nic"))
		})

		It("syncDeviceAttributes rejects colliding normalized names before writing", func() {
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy2", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "INTEL_NIC"},
					},
				},
			}
			beforeEachDRA()
			Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(MatchError(ContainSubstring("normalizes to the same DRA device class name")))
			attrList := &sriovdrav1alpha1.DeviceAttributesList{}
			Expect(r.List(ctx, attrList, k8sclient.InNamespace(testNamespace),
				k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(attrList.Items).To(BeEmpty())
		})

		It("syncSriovResourcePolicies rejects colliding normalized names before writing", func() {
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
					{
						ObjectMeta: metav1.ObjectMeta{Name: "policy2", Namespace: testNamespace},
						Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
							ResourceName: "INTEL_NIC",
							NodeSelector: map[string]string{"node-role.kubernetes.io/worker": ""},
							NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
						},
					},
				},
			}
			nl := &corev1.NodeList{Items: []corev1.Node{*node}}
			Expect(r.syncSriovResourcePolicies(ctx, dc, pl, nl)).To(MatchError(ContainSubstring("normalizes to the same DRA device class name")))
			policy := &sriovdrav1alpha1.SriovResourcePolicy{}
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: nodeName}, policy))).To(BeTrue())
		})

		It("syncDeviceAttributes removes DeviceAttributes when resource name no longer in policies", func() {
			attrLabels := drapkg.OperatorGeneratedByLabels()
			attrLabels[drapkg.ResourcePoolLabel] = "old"
			attr := &sriovdrav1alpha1.DeviceAttributes{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "old-attrs",
					Namespace: testNamespace,
					Labels:    attrLabels,
				},
				Spec: sriovdrav1alpha1.DeviceAttributesSpec{Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}},
			}
			beforeEachDRA(attr)
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{Items: []sriovnetworkv1.SriovNetworkNodePolicy{}}
			Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
			err := r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "old-attrs"}, attr)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("syncDeviceAttributes prunes obsolete names despite pool label drift", func() {
			beforeEachDRA()
			retained := buildDeviceAttributesCR("intel-nic-attrs", "intel_nic")
			retained.UID = "retained-attributes"
			retained.Labels[drapkg.ResourcePoolLabel] = "wrong-pool"
			obsolete := buildDeviceAttributesCR("zzz-attrs", "zzz")
			obsolete.Labels[drapkg.ResourcePoolLabel] = "intel-nic"
			foreign := buildDeviceAttributesCR("user-attrs", "user")
			foreign.Labels = map[string]string{drapkg.ResourcePoolLabel: "intel-nic"}
			for _, attr := range []*sriovdrav1alpha1.DeviceAttributes{retained, obsolete, foreign} {
				Expect(r.Create(ctx, attr)).To(Succeed())
			}
			Expect(r.Get(ctx, k8sclient.ObjectKeyFromObject(foreign), foreign)).To(Succeed())
			policies := &sriovnetworkv1.SriovNetworkNodePolicyList{Items: []sriovnetworkv1.SriovNetworkNodePolicy{{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: testNamespace},
				Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
			}}}
			for range 2 {
				Expect(r.syncDeviceAttributes(ctx, dc, policies)).To(Succeed())
				got := &sriovdrav1alpha1.DeviceAttributes{}
				Expect(errors.IsNotFound(r.Get(ctx, k8sclient.ObjectKeyFromObject(obsolete), got))).To(BeTrue())
				Expect(r.Get(ctx, k8sclient.ObjectKeyFromObject(retained), got)).To(Succeed())
				Expect(got.UID).To(Equal(retained.UID), "the desired object must be repaired in place")
				Expect(got.Labels[drapkg.ResourcePoolLabel]).To(Equal("intel-nic"))
				Expect(got.Spec).To(Equal(retained.Spec))
				gotForeign := &sriovdrav1alpha1.DeviceAttributes{}
				Expect(r.Get(ctx, k8sclient.ObjectKeyFromObject(foreign), gotForeign)).To(Succeed())
				Expect(gotForeign).To(Equal(foreign))
			}
		})

		It("syncSriovResourcePolicies skips nodes missing kubernetes.io/hostname", func() {
			nodeName := "worker-unlabeled"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						"node-role.kubernetes.io/worker": "",
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
			policyList := &sriovdrav1alpha1.SriovResourcePolicyList{}
			Expect(r.List(ctx, policyList, k8sclient.InNamespace(testNamespace),
				k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(policyList.Items).To(BeEmpty())
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
			Expect(policy.Spec.Configs[0].DeviceAttributesSelector.MatchLabels).To(HaveKeyWithValue(drapkg.ResourcePoolLabel, "intel-nic"))
			Expect(policy.Spec.Configs[0].ResourceFilters).To(HaveLen(1))
			Expect(policy.Spec.Configs[0].ResourceFilters[0].Vendors).To(Equal([]string{"8086"}))
		})

		It("syncExtendedResourceDeviceClasses rejects colliding normalized names before writing", func() {
			beforeEachDRA()
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "INTEL_NIC"},
					},
				},
			}
			Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(MatchError(ContainSubstring("normalizes to the same DRA device class name")))
			dcList := &resourceapi.DeviceClassList{}
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(dcList.Items).To(BeEmpty())
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
			dcList := &resourceapi.DeviceClassList{}
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(dcList.Items).To(HaveLen(1))
			Expect(dcList.Items[0].Name).To(Equal("intel-nic"))
			Expect(dcList.Items[0].Spec.ExtendedResourceName).NotTo(BeNil())
			Expect(*dcList.Items[0].Spec.ExtendedResourceName).To(Equal("openshift.io/intel_nic"))
		})

		It("cleanupExtendedResourceDeviceClasses deletes operator-created DeviceClasses", func() {
			labels := drapkg.OperatorGeneratedByLabels()
			labels[drapkg.DeviceClassResourceNameLabel] = "intel_nic"
			deviceClass := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "intel-nic",
					Labels: labels,
				},
			}
			beforeEachDRA(deviceClass)
			Expect(cleanupExtendedResourceDeviceClasses(ctx, r.Client)).To(Succeed())
			dcList := &resourceapi.DeviceClassList{}
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(dcList.Items).To(BeEmpty())
		})

		It("cleanupExtendedResourceDeviceClasses deletes extended DeviceClasses missing the resource-name label", func() {
			base := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:   consts.DRADriverBaseDeviceClassName,
					Labels: drapkg.OperatorGeneratedByLabels(),
				},
			}
			orphan := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "orphan-nic",
					Labels: drapkg.OperatorGeneratedByLabels(),
				},
			}
			beforeEachDRA(base, orphan)
			Expect(cleanupExtendedResourceDeviceClasses(ctx, r.Client)).To(Succeed())
			Expect(r.Get(ctx, types.NamespacedName{Name: consts.DRADriverBaseDeviceClassName}, &resourceapi.DeviceClass{})).To(Succeed())
			Expect(r.Get(ctx, types.NamespacedName{Name: "orphan-nic"}, &resourceapi.DeviceClass{})).To(MatchError(ContainSubstring("not found")))
		})

		It("syncExtendedResourceDeviceClasses does not delete the base DeviceClass", func() {
			base := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:   consts.DRADriverBaseDeviceClassName,
					Labels: drapkg.OperatorGeneratedByLabels(),
				},
			}
			beforeEachDRA(base)
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(Succeed())
			got := &resourceapi.DeviceClass{}
			Expect(r.Get(ctx, types.NamespacedName{Name: consts.DRADriverBaseDeviceClassName}, got)).To(Succeed())
			Expect(got.Labels[drapkg.GeneratedByLabel]).To(Equal(consts.SriovNetworkOperatorIdentifier))
			Expect(got.Labels).NotTo(HaveKey(drapkg.DeviceClassResourceNameLabel))
		})

		It("syncExtendedResourceDeviceClasses deletes obsolete DeviceClasses missing the resource-name label", func() {
			base := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:   consts.DRADriverBaseDeviceClassName,
					Labels: drapkg.OperatorGeneratedByLabels(),
				},
			}
			orphan := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "orphan-nic",
					Labels: drapkg.OperatorGeneratedByLabels(),
				},
			}
			beforeEachDRA(base, orphan)
			pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
				Items: []sriovnetworkv1.SriovNetworkNodePolicy{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"},
					},
				},
			}
			Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(Succeed())
			Expect(r.Get(ctx, types.NamespacedName{Name: consts.DRADriverBaseDeviceClassName}, &resourceapi.DeviceClass{})).To(Succeed())
			Expect(r.Get(ctx, types.NamespacedName{Name: "intel-nic"}, &resourceapi.DeviceClass{})).To(Succeed())
			Expect(r.Get(ctx, types.NamespacedName{Name: "orphan-nic"}, &resourceapi.DeviceClass{})).To(MatchError(ContainSubstring("not found")))
		})

		It("cleanupExtendedResourceDeviceClasses leaves the base DeviceClass", func() {
			base := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:   consts.DRADriverBaseDeviceClassName,
					Labels: drapkg.OperatorGeneratedByLabels(),
				},
			}
			beforeEachDRA(base)
			Expect(cleanupExtendedResourceDeviceClasses(ctx, r.Client)).To(Succeed())
			got := &resourceapi.DeviceClass{}
			Expect(r.Get(ctx, types.NamespacedName{Name: consts.DRADriverBaseDeviceClassName}, got)).To(Succeed())
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
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: testNamespace, UID: "operator-owner"},
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
			Expect(cleanupExtendedResourceDeviceClasses(ctx, r.Client)).To(Succeed())
		})

		It("cleanupSriovResourcePoliciesAndDeviceAttributes deletes operator-created policies and attributes", func() {
			policy := &sriovdrav1alpha1.SriovResourcePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: testNamespace,
					Labels:    drapkg.OperatorGeneratedByLabels(),
				},
			}
			attr := &sriovdrav1alpha1.DeviceAttributes{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "intel-nic-attrs",
					Namespace: testNamespace,
					Labels:    drapkg.OperatorGeneratedByLabels(),
				},
			}
			beforeEachDRA(policy, attr)
			Expect(r.cleanupSriovResourcePoliciesAndDeviceAttributes(ctx, dc)).To(Succeed())
			var gotPolicy sriovdrav1alpha1.SriovResourcePolicy
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "foo"}, &gotPolicy))).To(BeTrue())
			var gotAttr sriovdrav1alpha1.DeviceAttributes
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "intel-nic-attrs"}, &gotAttr))).To(BeTrue())
		})
	})

})

func TestPolicyReconcileCleansDeviceClassesWithoutActiveConfig(t *testing.T) {
	for _, terminating := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminating_%t", terminating), func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{sriovnetworkv1.AddToScheme, resourceapi.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}
			config := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
				Name: consts.DefaultConfigName, Namespace: vars.Namespace,
				Finalizers:        []string{sriovnetworkv1.OPERATORCONFIGFINALIZERNAME},
				DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
			}, Spec: sriovnetworkv1.SriovOperatorConfigSpec{
				FeatureGates: map[string]bool{consts.DynamicResourceAllocationFeatureGate: true},
			}}
			generated := &resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{
				Name: "intel-nic", Labels: drapkg.OperatorGeneratedByLabels(),
			}}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(generated)
			if terminating {
				builder.WithObjects(config)
			}
			cl := builder.Build()
			// No feature gate is needed: deletion must exit before normal reconciliation.
			r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme}
			for i := 0; i < 2; i++ {
				result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodePolicySyncEventName}})
				if err != nil {
					t.Fatal(err)
				}
				if result.RequeueAfter == 0 {
					t.Fatal("expected retry while waiting for an active config")
				}
				if err := cl.Get(ctx, k8sclient.ObjectKeyFromObject(generated), &resourceapi.DeviceClass{}); !errors.IsNotFound(err) {
					t.Fatalf("generated DeviceClass survived cleanup: %v", err)
				}
				if i == 0 {
					// Simulate an in-flight reconciliation recreating a class after cleanup.
					generated.ResourceVersion = ""
					if err := cl.Create(ctx, generated); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

// TestReconcileCleansDRAObjectsWhenFeatureGateDisabled verifies that Reconcile
// uses SriovOperatorConfig before choosing the DRA path without changing the
// shared gate, which may reflect a newer config-controller reconciliation.
func TestReconcileCleansDRAObjectsWhenFeatureGateDisabled(t *testing.T) {
	ctx := context.Background()
	nsSaved := vars.Namespace
	prefixSaved := vars.ResourcePrefix
	vars.Namespace = testNamespace
	vars.ResourcePrefix = "openshift.io"
	defer func() {
		vars.Namespace = nsSaved
		vars.ResourcePrefix = prefixSaved
	}()

	scheme := runtime.NewScheme()
	utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
	utilruntime.Must(sriovdrav1alpha1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(resourceapi.AddToScheme(scheme))

	dc := &sriovnetworkv1.SriovOperatorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: testNamespace},
		Status:     sriovnetworkv1.SriovOperatorConfigStatus{ResourceProvider: sriovnetworkv1.DevicePluginResourceProvider()},
		Spec: sriovnetworkv1.SriovOperatorConfigSpec{
			FeatureGates: map[string]bool{consts.DynamicResourceAllocationFeatureGate: false},
		},
	}
	attr := &sriovdrav1alpha1.DeviceAttributes{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "intel-nic-attrs",
			Namespace: testNamespace,
			Labels:    drapkg.OperatorGeneratedByLabels(),
		},
	}
	policy := &sriovdrav1alpha1.SriovResourcePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: testNamespace,
			Labels:    drapkg.OperatorGeneratedByLabels(),
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dc, attr, policy).Build()
	fg := featuregate.New()
	fg.Init(map[string]bool{consts.DynamicResourceAllocationFeatureGate: true})
	r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme, FeatureGate: fg}

	if !r.FeatureGate.IsEnabled(consts.DynamicResourceAllocationFeatureGate) {
		t.Fatal("precondition: FeatureGate should still report DRA enabled")
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodePolicySyncEventName}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !fg.IsEnabled(consts.DynamicResourceAllocationFeatureGate) {
		t.Fatal("policy reconcile must not overwrite the shared FeatureGate")
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: consts.ConfigMapName}, &corev1.ConfigMap{}); err != nil {
		t.Fatalf("expected device plugin ConfigMap recreated when DRA is disabled: %v", err)
	}
	gotAttr := &sriovdrav1alpha1.DeviceAttributes{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "intel-nic-attrs"}, gotAttr); !errors.IsNotFound(err) {
		t.Fatalf("expected DeviceAttributes deleted, get err=%v", err)
	}
	gotPolicy := &sriovdrav1alpha1.SriovResourcePolicy{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "worker-0"}, gotPolicy); !errors.IsNotFound(err) {
		t.Fatalf("expected SriovResourcePolicy deleted, get err=%v", err)
	}
}

func TestPolicyReconcileUsesConfigSnapshot(t *testing.T) {
	oldNamespace, oldPrefix := vars.Namespace, vars.ResourcePrefix
	vars.Namespace, vars.ResourcePrefix = testNamespace, "openshift.io"
	t.Cleanup(func() { vars.Namespace, vars.ResourcePrefix = oldNamespace, oldPrefix })

	for _, acceptedDRA := range []bool{false, true} {
		t.Run(fmt.Sprintf("acceptedDRA=%t", acceptedDRA), func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{sriovnetworkv1.AddToScheme, sriovdrav1alpha1.AddToScheme, corev1.AddToScheme, resourceapi.AddToScheme} {
				utilruntime.Must(add(scheme))
			}
			config := &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: vars.Namespace, UID: "operator-owner"},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					FeatureGates: map[string]bool{
						consts.DynamicResourceAllocationFeatureGate: acceptedDRA,
						consts.ManageSoftwareBridgesFeatureGate:     acceptedDRA,
					},
				},
				Status: sriovnetworkv1.SriovOperatorConfigStatus{ResourceProvider: sriovnetworkv1.DevicePluginResourceProvider()},
			}
			if acceptedDRA {
				config.Status.ResourceProvider = sriovnetworkv1.DRAResourceProvider()
			}
			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: vars.Namespace},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "intel_nic", DeviceType: "netdevice", NumVfs: 2,
					NicSelector: sriovnetworkv1.SriovNetworkNicSelector{PfNames: []string{"ens1"}},
					EswitchMode: sriovnetworkv1.ESwithModeSwitchDev,
					Bridge:      sriovnetworkv1.Bridge{OVS: &sriovnetworkv1.OVSConfig{}},
				},
			}
			node := testManagedNode("worker")
			node.Labels[corev1.LabelHostname] = node.Name
			state := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: vars.Namespace},
				Status: sriovnetworkv1.SriovNetworkNodeStateStatus{Interfaces: sriovnetworkv1.InterfaceExts{{
					Name: "ens1", PciAddress: "0000:01:00.0", TotalVfs: 8,
				}}},
			}
			fg := featuregate.New()
			gateChanged := false
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, policy, node, state).WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c k8sclient.WithWatch, list k8sclient.ObjectList, opts ...k8sclient.ListOption) error {
					if err := c.List(ctx, list, opts...); err != nil {
						return err
					}
					if _, ok := list.(*sriovnetworkv1.SriovNetworkNodePolicyList); ok {
						// Simulate another controller updating the shared gate after
						// this reconcile has accepted its provider and read policies.
						fg.Init(map[string]bool{
							consts.DynamicResourceAllocationFeatureGate: !acceptedDRA,
							consts.ManageSoftwareBridgesFeatureGate:     !acceptedDRA,
						})
						gateChanged = true
					}
					return nil
				},
			}).Build()
			r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme, FeatureGate: fg}
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nodePolicySyncEventName}}); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if !gateChanged {
				t.Fatal("shared gate was not changed during reconcile")
			}
			if err := cl.Get(ctx, k8sclient.ObjectKeyFromObject(state), state); err != nil {
				t.Fatal(err)
			}
			if gotBridges := len(state.Spec.Bridges.OVS) > 0; gotBridges != acceptedDRA {
				t.Errorf("bridges rendered = %t, want %t from config snapshot", gotBridges, acceptedDRA)
			}
			attrs := &sriovdrav1alpha1.DeviceAttributesList{}
			if err := cl.List(ctx, attrs); err != nil {
				t.Fatal(err)
			}
			if gotDRA := len(attrs.Items) > 0; gotDRA != acceptedDRA {
				t.Errorf("DRA resources present = %t, want %t", gotDRA, acceptedDRA)
			}
			err := cl.Get(ctx, types.NamespacedName{Namespace: vars.Namespace, Name: consts.ConfigMapName}, &corev1.ConfigMap{})
			if acceptedDRA && !errors.IsNotFound(err) || !acceptedDRA && err != nil {
				t.Errorf("device-plugin ConfigMap lookup = %v, acceptedDRA = %t", err, acceptedDRA)
			}
		})
	}
}

func TestSyncNamespacedDRAOwnership(t *testing.T) {
	ctx := context.Background()
	oldNamespace := vars.Namespace
	vars.Namespace = "dra-ownership-test"
	t.Cleanup(func() { vars.Namespace = oldNamespace })
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{sriovnetworkv1.AddToScheme, sriovdrav1alpha1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	owner := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
		Name: consts.DefaultConfigName, Namespace: vars.Namespace, UID: "operator-owner",
	}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Labels: map[string]string{corev1.LabelHostname: "worker-1"}}}
	state := &sriovnetworkv1.SriovNetworkNodeState{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: vars.Namespace}}
	policies := &sriovnetworkv1.SriovNetworkNodePolicyList{Items: []sriovnetworkv1.SriovNetworkNodePolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec:       sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic", NicSelector: sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"}},
	}}}
	userValue := "user-value"
	for _, original := range []k8sclient.Object{
		&sriovdrav1alpha1.DeviceAttributes{
			ObjectMeta: metav1.ObjectMeta{Name: "intel-nic-attrs", Namespace: vars.Namespace},
			Spec: sriovdrav1alpha1.DeviceAttributesSpec{Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				"example.com/custom": {StringValue: &userValue},
			}},
		},
		&sriovdrav1alpha1.SriovResourcePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: vars.Namespace},
			Spec: sriovdrav1alpha1.SriovResourcePolicySpec{Configs: []sriovdrav1alpha1.Config{{
				ResourceFilters: []sriovdrav1alpha1.ResourceFilter{{Vendors: []string{"15b3"}}},
			}}},
		},
	} {
		for _, marker := range []string{"none", "controller", "label", "foreign-controller"} {
			t.Run(original.GetName()+"/"+marker, func(t *testing.T) {
				existing := original.DeepCopyObject().(k8sclient.Object)
				switch marker {
				case "controller", "foreign-controller":
					controllerOwner := owner.DeepCopy()
					if marker == "foreign-controller" {
						controllerOwner.Name, controllerOwner.UID = "other", "other-owner"
					}
					if err := controllerutil.SetControllerReference(controllerOwner, existing, scheme); err != nil {
						t.Fatal(err)
					}
				case "label":
					existing.SetLabels(drapkg.OperatorGeneratedByLabels())
				}
				cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner, node, state, existing).Build()
				r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme}
				before := existing.DeepCopyObject().(k8sclient.Object)
				if err := cl.Get(ctx, k8sclient.ObjectKeyFromObject(existing), before); err != nil {
					t.Fatal(err)
				}
				var err error
				switch existing.(type) {
				case *sriovdrav1alpha1.DeviceAttributes:
					err = r.syncDeviceAttributes(ctx, owner, policies)
				case *sriovdrav1alpha1.SriovResourcePolicy:
					obsolete := &sriovdrav1alpha1.SriovResourcePolicy{ObjectMeta: metav1.ObjectMeta{
						Name: "old-worker", Namespace: vars.Namespace, Labels: drapkg.OperatorGeneratedByLabels(),
					}}
					if createErr := cl.Create(ctx, obsolete); createErr != nil {
						t.Fatal(createErr)
					}
					err = r.syncSriovResourcePolicies(ctx, owner, policies, &corev1.NodeList{Items: []corev1.Node{*node}})
					if getErr := cl.Get(ctx, k8sclient.ObjectKeyFromObject(obsolete), obsolete); !errors.IsNotFound(getErr) {
						t.Errorf("obsolete managed policy must be pruned even on ownership conflict, get error: %v", getErr)
					}
				}
				got := existing.DeepCopyObject().(k8sclient.Object)
				if getErr := cl.Get(ctx, k8sclient.ObjectKeyFromObject(existing), got); getErr != nil {
					t.Fatal(getErr)
				}
				if marker == "none" || marker == "foreign-controller" {
					if !errors.IsConflict(err) {
						t.Fatalf("expected ownership conflict, got %v", err)
					}
					if diff := cmp.Diff(before, got); diff != "" {
						t.Fatalf("user-owned object changed (-before +after):\n%s", diff)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !metav1.IsControlledBy(got, owner) || got.GetLabels()[drapkg.GeneratedByLabel] != consts.SriovNetworkOperatorIdentifier {
					t.Fatalf("operator ownership was not restored: %+v", got)
				}
			})
		}
	}
}

func TestPolicyReconcileRejectsUnsafeConfigurationBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name, resource, deviceType, wantError string
		netFilter                             string
		pfNames                               []string
		blockedSwitch                         bool
	}{
		{name: "normalized collision", resource: "INTEL_NIC", wantError: "same DRA device class"},
		{name: "mixed device types", resource: "intel_nic", deviceType: "vfio-pci", wantError: "incompatible deviceType"},
		{name: "empty resource", resource: "", wantError: "must not be empty"},
		{name: "VF range", resource: "other", pfNames: []string{"ens1f0#2-5"}, wantError: "VF range"},
		{name: "netFilter", resource: "other", netFilter: "aws/NetworkID:net-1", wantError: "netFilter"},
		{name: "unaccepted provider", resource: "other", blockedSwitch: true, wantError: "has not been accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{sriovnetworkv1.AddToScheme, corev1.AddToScheme, resourceapi.AddToScheme, sriovdrav1alpha1.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}
			config := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: vars.Namespace}, Spec: sriovnetworkv1.SriovOperatorConfigSpec{FeatureGates: map[string]bool{consts.DynamicResourceAllocationFeatureGate: true}}, Status: sriovnetworkv1.SriovOperatorConfigStatus{ResourceProvider: sriovnetworkv1.DRAResourceProvider()}}
			if tc.blockedSwitch {
				config.Status.ResourceProvider = sriovnetworkv1.DevicePluginResourceProvider()
			}
			first := &sriovnetworkv1.SriovNetworkNodePolicy{ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: vars.Namespace}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic", DeviceType: "netdevice"}}
			second := &sriovnetworkv1.SriovNetworkNodePolicy{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: vars.Namespace}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: tc.resource, DeviceType: tc.deviceType}}
			second.Spec.NicSelector.PfNames = tc.pfNames
			second.Spec.NicSelector.NetFilter = tc.netFilter
			writes := 0
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, first, second).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(context.Context, k8sclient.WithWatch, k8sclient.Object, ...k8sclient.CreateOption) error {
					writes++
					return fmt.Errorf("unexpected create")
				},
				Update: func(context.Context, k8sclient.WithWatch, k8sclient.Object, ...k8sclient.UpdateOption) error {
					writes++
					return fmt.Errorf("unexpected update")
				},
				Patch: func(context.Context, k8sclient.WithWatch, k8sclient.Object, k8sclient.Patch, ...k8sclient.PatchOption) error {
					writes++
					return fmt.Errorf("unexpected patch")
				},
				Delete: func(context.Context, k8sclient.WithWatch, k8sclient.Object, ...k8sclient.DeleteOption) error {
					writes++
					return fmt.Errorf("unexpected delete")
				},
				DeleteAllOf: func(context.Context, k8sclient.WithWatch, k8sclient.Object, ...k8sclient.DeleteAllOfOption) error {
					writes++
					return fmt.Errorf("unexpected delete all")
				},
			}).Build()
			r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme, FeatureGate: featuregate.New()}
			_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: nodePolicySyncEventName}})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Reconcile error = %v, want %q", err, tc.wantError)
			}
			if writes != 0 {
				t.Fatalf("rejected configuration attempted %d writes", writes)
			}
		})
	}
}

func TestDRADeletionPreconditions(t *testing.T) {
	oldNamespace := vars.Namespace
	vars.Namespace = testNamespace
	t.Cleanup(func() { vars.Namespace = oldNamespace })
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{sriovnetworkv1.AddToScheme, sriovdrav1alpha1.AddToScheme, resourceapi.AddToScheme} {
		utilruntime.Must(add(scheme))
	}
	owner := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
		Name: consts.DefaultConfigName, Namespace: vars.Namespace, UID: "operator-owner",
	}}
	for _, mode := range []string{"prune", "disable"} {
		for _, template := range []k8sclient.Object{&sriovdrav1alpha1.DeviceAttributes{}, &sriovdrav1alpha1.SriovResourcePolicy{}, &resourceapi.DeviceClass{}} {
			for _, change := range []string{"unchanged", "replacement", "ownership removed", "managed update", "already deleted"} {
				t.Run(fmt.Sprintf("%s/%T/%s", mode, template, change), func(t *testing.T) {
					g := NewWithT(t)
					ctx := context.Background()
					original := template.DeepCopyObject().(k8sclient.Object)
					original.SetName("obsolete")
					original.SetUID("original")
					original.SetLabels(drapkg.OperatorGeneratedByLabels())
					if _, clusterScoped := original.(*resourceapi.DeviceClass); !clusterScoped {
						original.SetNamespace(vars.Namespace)
					}
					deleteCalls := 0
					cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner, original).WithInterceptorFuncs(interceptor.Funcs{
						Delete: func(ctx context.Context, c k8sclient.WithWatch, obj k8sclient.Object, opts ...k8sclient.DeleteOption) error {
							deleteCalls++
							options := (&k8sclient.DeleteOptions{}).ApplyOptions(opts)
							g.Expect(options.Preconditions).NotTo(BeNil())
							g.Expect(options.Preconditions.UID).To(HaveValue(Equal(obj.GetUID())))
							g.Expect(options.Preconditions.ResourceVersion).To(HaveValue(Equal(obj.GetResourceVersion())))
							if deleteCalls == 1 {
								live := original.DeepCopyObject().(k8sclient.Object)
								g.Expect(c.Get(ctx, k8sclient.ObjectKeyFromObject(original), live)).To(Succeed())
								switch change {
								case "replacement", "already deleted":
									g.Expect(c.Delete(ctx, live)).To(Succeed())
									if change == "replacement" {
										live.SetUID("replacement")
										live.SetResourceVersion("")
										live.SetLabels(nil)
										g.Expect(c.Create(ctx, live)).To(Succeed())
									}
								case "ownership removed":
									live.SetLabels(nil)
									g.Expect(c.Update(ctx, live)).To(Succeed())
								case "managed update":
									live.SetAnnotations(map[string]string{"updated": "true"})
									g.Expect(c.Update(ctx, live)).To(Succeed())
								}
							}
							return c.Delete(ctx, obj, opts...)
						},
					}).Build()
					r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme}
					sync := func() error {
						policies := &sriovnetworkv1.SriovNetworkNodePolicyList{}
						if mode == "prune" {
							switch original.(type) {
							case *sriovdrav1alpha1.DeviceAttributes:
								return r.syncDeviceAttributes(ctx, owner, policies)
							case *sriovdrav1alpha1.SriovResourcePolicy:
								return r.syncSriovResourcePolicies(ctx, owner, policies, &corev1.NodeList{})
							default:
								return r.syncExtendedResourceDeviceClasses(ctx, owner, policies)
							}
						}
						if _, clusterScoped := original.(*resourceapi.DeviceClass); clusterScoped {
							return cleanupExtendedResourceDeviceClasses(ctx, cl)
						}
						return r.cleanupSriovResourcePoliciesAndDeviceAttributes(ctx, owner)
					}
					err := sync()
					if change == "unchanged" || change == "already deleted" {
						g.Expect(err).NotTo(HaveOccurred())
					} else {
						g.Expect(errors.IsConflict(err)).To(BeTrue(), "a changed object must reject the stale delete: %v", err)
					}
					preserve := change == "replacement" || change == "ownership removed"
					before := original.DeepCopyObject().(k8sclient.Object)
					if preserve || change == "managed update" {
						g.Expect(cl.Get(ctx, k8sclient.ObjectKeyFromObject(original), before)).To(Succeed())
					}
					// The next reconciliation must re-list and check ownership again.
					g.Expect(sync()).To(Succeed())
					after := original.DeepCopyObject().(k8sclient.Object)
					getErr := cl.Get(ctx, k8sclient.ObjectKeyFromObject(original), after)
					if preserve {
						g.Expect(getErr).NotTo(HaveOccurred())
						g.Expect(after).To(Equal(before))
					} else {
						g.Expect(errors.IsNotFound(getErr)).To(BeTrue())
					}
					expectedDeletes := 1
					if change == "managed update" {
						expectedDeletes = 2
					}
					g.Expect(deleteCalls).To(Equal(expectedDeletes))
				})
			}
		}
	}
}

func TestDRAObjectCleanupUsesRepairOwnershipMarkers(t *testing.T) {
	oldNamespace := vars.Namespace
	vars.Namespace = testNamespace
	t.Cleanup(func() { vars.Namespace = oldNamespace })
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{sriovnetworkv1.AddToScheme, sriovdrav1alpha1.AddToScheme, resourceapi.AddToScheme} {
		utilruntime.Must(add(scheme))
	}
	owner := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
		Name: consts.DefaultConfigName, Namespace: vars.Namespace, UID: "operator-owner",
	}}

	for _, mode := range []string{"prune", "disable"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var objects []k8sclient.Object
			var removed, preserved []k8sclient.Object
			for _, template := range []k8sclient.Object{&sriovdrav1alpha1.DeviceAttributes{}, &sriovdrav1alpha1.SriovResourcePolicy{}} {
				for _, marker := range []string{"controller", "label", "unmarked", "foreign-controller", "foreign-controller-label", "other-namespace"} {
					obj := template.DeepCopyObject().(k8sclient.Object)
					obj.SetName(marker)
					obj.SetNamespace(vars.Namespace)
					switch marker {
					case "controller", "other-namespace":
						if err := controllerutil.SetControllerReference(owner, obj, scheme); err != nil {
							t.Fatal(err)
						}
						if marker == "other-namespace" {
							obj.SetNamespace("other-namespace")
						}
					case "label":
						obj.SetLabels(drapkg.OperatorGeneratedByLabels())
					case "foreign-controller", "foreign-controller-label":
						foreign := owner.DeepCopy()
						foreign.UID = "foreign-owner"
						if err := controllerutil.SetControllerReference(foreign, obj, scheme); err != nil {
							t.Fatal(err)
						}
						if marker == "foreign-controller-label" {
							obj.SetLabels(drapkg.OperatorGeneratedByLabels())
						}
					}
					objects = append(objects, obj)
					if marker == "controller" || marker == "label" {
						removed = append(removed, obj)
					} else {
						preserved = append(preserved, obj)
					}
				}
			}
			for _, tc := range []struct {
				name   string
				labels map[string]string
				remove bool
			}{
				{name: "intel-nic", labels: map[string]string{drapkg.DeviceClassResourceNameLabel: "intel_nic"}, remove: true},
				{name: "generated", labels: drapkg.OperatorGeneratedByLabels(), remove: true},
				{name: "user-class"},
				{name: "mismatched-name", labels: map[string]string{drapkg.DeviceClassResourceNameLabel: "intel_nic"}},
				{name: "empty-marker", labels: map[string]string{drapkg.DeviceClassResourceNameLabel: ""}},
				{name: consts.DRADriverBaseDeviceClassName, labels: drapkg.OperatorGeneratedByLabels()},
			} {
				obj := &resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: tc.name, Labels: tc.labels}}
				objects = append(objects, obj)
				if tc.remove {
					removed = append(removed, obj)
				} else {
					preserved = append(preserved, obj)
				}
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			r := &SriovNetworkNodePolicyReconciler{Client: cl, Scheme: scheme}
			// Capture complete stored objects so preservation includes specs and metadata.
			for _, obj := range preserved {
				if err := cl.Get(ctx, k8sclient.ObjectKeyFromObject(obj), obj); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if mode == "prune" {
					policies := &sriovnetworkv1.SriovNetworkNodePolicyList{}
					if err := r.syncDeviceAttributes(ctx, owner, policies); err != nil {
						t.Fatal(err)
					}
					if err := r.syncSriovResourcePolicies(ctx, owner, policies, &corev1.NodeList{}); err != nil {
						t.Fatal(err)
					}
					if err := r.syncExtendedResourceDeviceClasses(ctx, owner, policies); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := r.cleanupSriovResourcePoliciesAndDeviceAttributes(ctx, owner); err != nil {
						t.Fatal(err)
					}
					if err := cleanupExtendedResourceDeviceClasses(ctx, cl); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, obj := range removed {
				if err := cl.Get(ctx, k8sclient.ObjectKeyFromObject(obj), obj); !errors.IsNotFound(err) {
					t.Errorf("obsolete %T %s survived cleanup: %v", obj, obj.GetName(), err)
				}
			}
			for _, before := range preserved {
				got := before.DeepCopyObject().(k8sclient.Object)
				if err := cl.Get(ctx, k8sclient.ObjectKeyFromObject(before), got); err != nil {
					t.Errorf("preserved %T %s: %v", before, before.GetName(), err)
					continue
				}
				if diff := cmp.Diff(before, got); diff != "" {
					t.Errorf("preserved object changed (-before +after):\n%s", diff)
				}
			}
		})
	}
}

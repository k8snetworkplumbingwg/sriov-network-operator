package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/google/go-cmp/cmp"
	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

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

func TestCollectDRAPolicyResourceNames(t *testing.T) {
	pl := &sriovnetworkv1.SriovNetworkNodePolicyList{
		Items: []sriovnetworkv1.SriovNetworkNodePolicy{
			{ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultPolicyName}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "p1"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "intel_nic"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "p2"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: ""}},
			{ObjectMeta: metav1.ObjectMeta{Name: "p3"}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "mlx5"}},
		},
	}
	got := collectDRAPolicyResourceNames(pl)
	if len(got) != 2 {
		t.Fatalf("collectDRAPolicyResourceNames() returned %d names, want 2", len(got))
	}
	if _, ok := got["intel_nic"]; !ok {
		t.Error("expected intel_nic in result")
	}
	if _, ok := got["mlx5"]; !ok {
		t.Error("expected mlx5 in result")
	}
}

func TestFilterDRAResourceNamesByDeviceClass(t *testing.T) {
	names := map[string]struct{}{
		"intel_nic": {},
		"INTEL_NIC": {},
		"mlx5":      {},
	}
	got := filterDRAResourceNamesByDeviceClass(log.Log, names)
	if len(got) != 2 {
		t.Fatalf("filterDRAResourceNamesByDeviceClass() returned %d names, want 2", len(got))
	}
	if got[0].resourceName != "INTEL_NIC" || got[0].deviceClassName != "intel-nic" {
		t.Fatalf("filterDRAResourceNamesByDeviceClass()[0] = %+v, want INTEL_NIC/intel-nic (sorted, first collision wins)", got[0])
	}
	if got[1].resourceName != "mlx5" || got[1].deviceClassName != "mlx5" {
		t.Fatalf("filterDRAResourceNamesByDeviceClass()[1] = %+v, want mlx5/mlx5", got[1])
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
	poolLabel := "intel-nic"
	deviceIDPassthrough := "154c"
	deviceIDVf := "10de"
	pfNames := []string{"ens1f0"}
	rootDevices := []string{"0000:08:00.0"}

	testCases := []struct {
		name      string
		policy    *sriovnetworkv1.SriovNetworkNodePolicy
		nodeState *sriovnetworkv1.SriovNetworkNodeState
		wantErr   bool
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
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{DeviceID: deviceIDVf},
					NumVfs:       4,
				},
			},
			nodeState: nil,
			check: func(t *testing.T, c *sriovdrav1alpha1.Config) {
				// GetVfDeviceID returns "" for unknown deviceID; skip the empty filter.
				if len(c.ResourceFilters) == 0 {
					return
				}
				if len(c.ResourceFilters) != 1 {
					t.Fatalf("expected one ResourceFilter or none, got %d", len(c.ResourceFilters))
				}
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
			cfg, err := buildPolicyConfig(tc.policy, tc.nodeState)
			if tc.wantErr {
				if err == nil {
					t.Fatal("buildPolicyConfig: expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildPolicyConfig: %v", err)
			}
			tc.check(t, cfg)
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

	obj := buildExtendedResourceDeviceClass(deviceClassName, resourceName, extendedResourceName, celExpr)

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
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: testNamespace},
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

			for range 3 {
				Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
			}
			attrList := &sriovdrav1alpha1.DeviceAttributesList{}
			Expect(r.List(ctx, attrList, k8sclient.InNamespace(testNamespace),
				k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(attrList.Items).To(HaveLen(1))

			for range 3 {
				Expect(r.syncSriovResourcePolicies(ctx, dc, pl, nl)).To(Succeed())
			}
			policyList := &sriovdrav1alpha1.SriovResourcePolicyList{}
			Expect(r.List(ctx, policyList, k8sclient.InNamespace(testNamespace),
				k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(policyList.Items).To(HaveLen(1))

			for range 3 {
				Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(Succeed())
			}
			dcList := &resourceapi.DeviceClassList{}
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(dcList.Items).To(HaveLen(1))

			for range 3 {
				Expect(r.cleanupSriovResourcePoliciesAndDeviceAttributes(ctx)).To(Succeed())
				Expect(r.cleanupExtendedResourceDeviceClasses(ctx)).To(Succeed())
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
			attr := buildDeviceAttributesCR("intel-nic-attrs", "intel_nic")
			attr.OwnerReferences = nil
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
			Expect(metav1.IsControlledBy(got, dc)).To(BeTrue())
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

		It("syncExtendedResourceDeviceClasses adopts CR when operator managed label was removed", func() {
			obj := buildExtendedResourceDeviceClass("intel-nic", "intel_nic", "openshift.io/intel_nic", buildDeviceClassCEL("intel_nic"))
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

		It("syncDeviceAttributes skips colliding normalized names in the same pass", func() {
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
			Expect(r.syncDeviceAttributes(ctx, dc, pl)).To(Succeed())
			attrList := &sriovdrav1alpha1.DeviceAttributesList{}
			Expect(r.List(ctx, attrList, k8sclient.InNamespace(testNamespace),
				k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(attrList.Items).To(HaveLen(1))
			Expect(attrList.Items[0].Name).To(Equal("intel-nic-attrs"))
		})

		It("syncSriovResourcePolicies skips colliding normalized names in the same pass", func() {
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
			Expect(r.syncSriovResourcePolicies(ctx, dc, pl, nl)).To(Succeed())
			policy := &sriovdrav1alpha1.SriovResourcePolicy{}
			Expect(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: nodeName}, policy)).To(Succeed())
			Expect(policy.Spec.Configs).To(HaveLen(1))
			Expect(policy.Spec.Configs[0].DeviceAttributesSelector.MatchLabels[drapkg.ResourcePoolLabel]).To(Equal("intel-nic"))
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

		It("syncExtendedResourceDeviceClasses skips colliding normalized names in the same pass", func() {
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
			Expect(r.syncExtendedResourceDeviceClasses(ctx, dc, pl)).To(Succeed())
			dcList := &resourceapi.DeviceClassList{}
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
			Expect(dcList.Items).To(HaveLen(1))
			Expect(dcList.Items[0].Name).To(Equal("intel-nic"))
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
			deviceClass := &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "intel-nic",
					Labels: drapkg.OperatorGeneratedByLabels(),
				},
			}
			beforeEachDRA(deviceClass)
			Expect(r.cleanupExtendedResourceDeviceClasses(ctx)).To(Succeed())
			dcList := &resourceapi.DeviceClassList{}
			Expect(r.List(ctx, dcList, k8sclient.MatchingLabels(drapkg.OperatorGeneratedByLabels()))).To(Succeed())
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
			Expect(r.cleanupSriovResourcePoliciesAndDeviceAttributes(ctx)).To(Succeed())
			var gotPolicy sriovdrav1alpha1.SriovResourcePolicy
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "foo"}, &gotPolicy))).To(BeTrue())
			var gotAttr sriovdrav1alpha1.DeviceAttributes
			Expect(errors.IsNotFound(r.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "intel-nic-attrs"}, &gotAttr))).To(BeTrue())
		})
	})

})

package webhook

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func setupDRANetFilterValidation(t *testing.T) (*sriovnetworkv1.SriovOperatorConfig, *sriovnetworkv1.SriovNetworkNodePolicy) {
	t.Helper()
	previousClient, previousNamespace := client, vars.Namespace
	t.Cleanup(func() {
		client, vars.Namespace = previousClient, previousNamespace
	})
	vars.Namespace = "sriov-network-operator"
	config := newDefaultOperatorConfig()
	config.Spec.DisableDrain = false
	config.Spec.FeatureGates = map[string]bool{consts.DynamicResourceAllocationFeatureGate: true}
	policy := newNodePolicy()
	policy.Namespace = vars.Namespace
	policy.Spec.NicSelector = sriovnetworkv1.SriovNetworkNicSelector{
		NetFilter: "openstack/NetworkID:ada9ec67-2c97-467c-b674-c47200e2f5da",
	}
	return config, policy
}

func TestValidateDRAModeChange(t *testing.T) {
	for _, tc := range []struct {
		name           string
		operation      admissionv1.Operation
		prepare        func(*sriovnetworkv1.SriovOperatorConfig, *sriovnetworkv1.SriovNetworkNodePolicy)
		noPolicy       bool
		alreadyEnabled bool
		noConfig       bool
		wantError      bool
	}{
		{name: "reject enabling DRA", operation: admissionv1.Update, wantError: true},
		{name: "reject enabling with missing config", operation: admissionv1.Update, noConfig: true, wantError: true},
		{name: "reject creating enabled config", operation: admissionv1.Create, wantError: true},
		{name: "allow updates with DRA already enabled", operation: admissionv1.Update, alreadyEnabled: true},
		{name: "allow deleting config", operation: admissionv1.Delete},
		{name: "reject disabling DRA", operation: admissionv1.Update, alreadyEnabled: true, wantError: true, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates[consts.DynamicResourceAllocationFeatureGate] = false
		}},
		{name: "reject removing DRA gate", operation: admissionv1.Update, alreadyEnabled: true, wantError: true, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			delete(c.Spec.FeatureGates, consts.DynamicResourceAllocationFeatureGate)
		}},
		{name: "reject removing feature gates map", operation: admissionv1.Update, alreadyEnabled: true, wantError: true, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates = nil
		}},
		{name: "allow disabling without policies", operation: admissionv1.Update, alreadyEnabled: true, noPolicy: true, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates[consts.DynamicResourceAllocationFeatureGate] = false
		}},
		{name: "allow removing gate without policies", operation: admissionv1.Update, alreadyEnabled: true, noPolicy: true, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates = nil
		}},
		{name: "allow updates with DRA already disabled", operation: admissionv1.Update, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates[consts.DynamicResourceAllocationFeatureGate] = false
		}},
		{name: "allow creating disabled config", operation: admissionv1.Create, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates = nil
		}},
		{name: "allow omitted gate", operation: admissionv1.Update, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates = nil
		}},
		{name: "allow no policies", operation: admissionv1.Update, noPolicy: true},
		{name: "allow creating enabled config without policies", operation: admissionv1.Create, noPolicy: true},
		{name: "reject enabling even with supported policy", operation: admissionv1.Update, wantError: true, prepare: func(_ *sriovnetworkv1.SriovOperatorConfig, p *sriovnetworkv1.SriovNetworkNodePolicy) {
			p.Spec.NicSelector = sriovnetworkv1.SriovNetworkNicSelector{PfNames: []string{"ens803f1"}}
		}},
		{name: "allow updates with existing supported DRA policy", operation: admissionv1.Update, alreadyEnabled: true, prepare: func(_ *sriovnetworkv1.SriovOperatorConfig, p *sriovnetworkv1.SriovNetworkNodePolicy) {
			p.Spec.NicSelector = sriovnetworkv1.SriovNetworkNicSelector{PfNames: []string{"ens803f1"}}
		}},
		{name: "ignore default policy", operation: admissionv1.Update, prepare: func(_ *sriovnetworkv1.SriovOperatorConfig, p *sriovnetworkv1.SriovNetworkNodePolicy) {
			p.Name = consts.DefaultPolicyName
		}},
		{name: "ignore other namespace", operation: admissionv1.Update, prepare: func(_ *sriovnetworkv1.SriovOperatorConfig, p *sriovnetworkv1.SriovNetworkNodePolicy) {
			p.Namespace = "other-namespace"
		}},
		{name: "ignore default policy when disabling", operation: admissionv1.Update, alreadyEnabled: true, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, p *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates = nil
			p.Name = consts.DefaultPolicyName
		}},
		{name: "ignore other namespace when disabling", operation: admissionv1.Update, alreadyEnabled: true, prepare: func(c *sriovnetworkv1.SriovOperatorConfig, p *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates = nil
			p.Namespace = "other-namespace"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			config, policy := setupDRANetFilterValidation(t)
			if tc.prepare != nil {
				tc.prepare(config, policy)
			}
			builder := fake.NewClientBuilder().WithScheme(vars.Scheme)
			if tc.operation == admissionv1.Update && !tc.noConfig {
				previousConfig := config.DeepCopy()
				previousConfig.Spec.FeatureGates = map[string]bool{consts.DynamicResourceAllocationFeatureGate: tc.alreadyEnabled}
				builder.WithObjects(previousConfig)
			}
			if !tc.noPolicy {
				builder.WithObjects(policy)
			}
			client = builder.Build()
			allowed, _, err := validateSriovOperatorConfig(config, tc.operation)
			g.Expect(allowed).To(Equal(!tc.wantError))
			if tc.wantError {
				g.Expect(err).To(MatchError(ContainSubstring("SriovNetworkNodePolicy " + policy.Namespace + "/" + policy.Name + " exists")))
			} else {
				g.Expect(err).NotTo(HaveOccurred())
			}
		})
	}
}

func TestValidateDRADisablementReadErrors(t *testing.T) {
	for _, failGet := range []bool{true, false} {
		t.Run(fmt.Sprintf("config lookup fails=%t", failGet), func(t *testing.T) {
			g := NewGomegaWithT(t)
			config, _ := setupDRANetFilterValidation(t)
			previousConfig := config.DeepCopy()
			config.Spec.FeatureGates = nil
			readError := fmt.Errorf("API unavailable")
			client = fake.NewClientBuilder().WithScheme(vars.Scheme).WithObjects(previousConfig).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c runtimeclient.WithWatch, key runtimeclient.ObjectKey, obj runtimeclient.Object, opts ...runtimeclient.GetOption) error {
					if failGet {
						return readError
					}
					return c.Get(ctx, key, obj, opts...)
				},
				List: func(context.Context, runtimeclient.WithWatch, runtimeclient.ObjectList, ...runtimeclient.ListOption) error {
					return readError
				},
			}).Build()
			allowed, _, err := validateSriovOperatorConfig(config, admissionv1.Update)
			g.Expect(allowed).To(BeFalse())
			g.Expect(err).To(MatchError(ContainSubstring(readError.Error())))
		})
	}
}

func TestValidateDRAPolicyNetFilter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prepare  func(*sriovnetworkv1.SriovOperatorConfig, *sriovnetworkv1.SriovNetworkNodePolicy)
		noConfig bool
	}{
		{name: "DRA disabled", prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates[consts.DynamicResourceAllocationFeatureGate] = false
		}},
		{name: "gate omitted", prepare: func(c *sriovnetworkv1.SriovOperatorConfig, _ *sriovnetworkv1.SriovNetworkNodePolicy) {
			c.Spec.FeatureGates = nil
		}},
		{name: "config missing", noConfig: true},
		{name: "netFilter removed", prepare: func(_ *sriovnetworkv1.SriovOperatorConfig, p *sriovnetworkv1.SriovNetworkNodePolicy) {
			p.Spec.NicSelector = sriovnetworkv1.SriovNetworkNicSelector{PfNames: []string{"ens803f1"}}
		}},
		{name: "other namespace", prepare: func(_ *sriovnetworkv1.SriovOperatorConfig, p *sriovnetworkv1.SriovNetworkNodePolicy) {
			p.Namespace = "other-namespace"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			config, policy := setupDRANetFilterValidation(t)
			if tc.prepare != nil {
				tc.prepare(config, policy)
			}
			builder := fake.NewClientBuilder().WithScheme(vars.Scheme)
			if !tc.noConfig {
				builder.WithObjects(config)
			}
			client = builder.Build()
			g.Expect(validateDRAPolicyNetFilter(policy)).To(Succeed())
		})
	}
}

func TestValidateSriovNetworkNodePolicyDRANetFilter(t *testing.T) {
	for _, operation := range []admissionv1.Operation{admissionv1.Create, admissionv1.Update, admissionv1.Delete} {
		t.Run(string(operation), func(t *testing.T) {
			g := NewGomegaWithT(t)
			config, policy := setupDRANetFilterValidation(t)
			client = fake.NewClientBuilder().WithScheme(vars.Scheme).WithObjects(config).Build()
			allowed, _, err := validateSriovNetworkNodePolicy(policy, operation)
			if operation == admissionv1.Delete {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(allowed).To(BeTrue())
			} else {
				g.Expect(allowed).To(BeFalse())
				g.Expect(err).To(MatchError(ContainSubstring("policy " + policy.Namespace + "/" + policy.Name + ": spec.nicSelector.netFilter is not supported")))
			}
		})
	}
}

func TestValidateDRANetFilterReadErrors(t *testing.T) {
	g := NewGomegaWithT(t)
	config, policy := setupDRANetFilterValidation(t)
	readError := fmt.Errorf("API unavailable")
	client = fake.NewClientBuilder().WithScheme(vars.Scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, runtimeclient.WithWatch, runtimeclient.ObjectKey, runtimeclient.Object, ...runtimeclient.GetOption) error {
			return readError
		},
		List: func(context.Context, runtimeclient.WithWatch, runtimeclient.ObjectList, ...runtimeclient.ListOption) error {
			return readError
		},
	}).Build()
	allowed, _, err := validateSriovOperatorConfig(config, admissionv1.Update)
	g.Expect(allowed).To(BeFalse())
	g.Expect(err).To(MatchError(ContainSubstring(readError.Error())))
	// CREATE skips the previous-config lookup, so this exercises a failed policy list.
	allowed, _, err = validateSriovOperatorConfig(config, admissionv1.Create)
	g.Expect(allowed).To(BeFalse())
	g.Expect(err).To(MatchError(ContainSubstring(readError.Error())))
	allowed, _, err = validateSriovNetworkNodePolicy(policy, admissionv1.Create)
	g.Expect(allowed).To(BeFalse())
	g.Expect(err).To(MatchError(ContainSubstring(readError.Error())))
}

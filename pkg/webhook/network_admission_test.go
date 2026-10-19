package webhook

import (
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func TestNetworkAdmissionFields(t *testing.T) {
	for _, kind := range []string{"SriovNetwork", "SriovIBNetwork", "OVSNetwork"} {
		for _, operation := range []admissionv1.Operation{admissionv1.Create, admissionv1.Update} {
			for _, tc := range []struct {
				name    string
				spec    map[string]any
				allowed bool
			}{
				{"defaults", map[string]any{}, true},
				{"valid", map[string]any{"capabilities": `{"mac":true}`, "ipam": `{}`, "metaPlugins": `{"type":"tuning"}`}, true},
				{"null capabilities", map[string]any{"capabilities": "null"}, false},
				{"null capability member", map[string]any{"capabilities": `{"mac":null}`}, false},
				{"null ipam", map[string]any{"ipam": "null"}, false},
				{"missing ipam type", map[string]any{"ipam": `{"subnet":"10.0.0.0/24"}`}, false},
				{"nested plugins", map[string]any{"metaPlugins": `[{"type":"tuning"}]`}, false},
				{"missing plugin type", map[string]any{"metaPlugins": `{}`}, false},
				{"namespace restriction", map[string]any{"networkNamespace": "other"}, false},
			} {
				t.Run(kind+"/"+string(operation)+"/"+tc.name, func(t *testing.T) {
					raw, err := json.Marshal(map[string]any{
						"apiVersion": "sriovnetwork.openshift.io/v1", "kind": kind,
						"metadata": map[string]any{"name": "test", "namespace": "tenant"},
						"spec":     tc.spec,
					})
					if err != nil {
						t.Fatal(err)
					}
					response := ValidateCustomResource(admissionv1.AdmissionReview{
						Request: &admissionv1.AdmissionRequest{
							Kind:      metav1.GroupVersionKind{Group: "sriovnetwork.openshift.io", Version: "v1", Kind: kind},
							Operation: operation, Object: runtime.RawExtension{Raw: raw},
						},
					})
					if response.Allowed != tc.allowed {
						t.Fatalf("allowed = %v, want %v: %+v", response.Allowed, tc.allowed, response.Result)
					}
				})
			}
		}
	}
}

func TestNetworkAdmissionLegacyFinalizerCleanup(t *testing.T) {
	for _, kind := range []string{"SriovNetwork", "SriovIBNetwork", "OVSNetwork"} {
		for _, tc := range []struct {
			name    string
			change  func(old, current map[string]any, request *admissionv1.AdmissionRequest)
			allowed bool
		}{
			{"legacy ipam", nil, true},
			{"legacy capabilities", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				for _, object := range []map[string]any{old, current} {
					object["spec"] = map[string]any{"capabilities": "null"}
				}
			}, true},
			{"changed invalid spec", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["spec"].(map[string]any)["ipam"] = "[]"
			}, false},
			{"valid correction", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["spec"].(map[string]any)["ipam"] = "{}"
			}, true},
			{"nonterminating", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				delete(old["metadata"].(map[string]any), "deletionTimestamp")
				delete(current["metadata"].(map[string]any), "deletionTimestamp")
			}, false},
			{"changed deletion timestamp", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-19T19:01:00Z"
			}, false},
			{"retained finalizer", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["metadata"].(map[string]any)["finalizers"] = old["metadata"].(map[string]any)["finalizers"]
			}, false},
			{"all finalizers removed", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["metadata"].(map[string]any)["finalizers"] = []string{}
			}, true},
			{"foreign finalizer removed before ours", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["metadata"].(map[string]any)["finalizers"] = []string{sriovnetworkv1.NETATTDEFFINALIZERNAME}
			}, true},
			{"GC finalizer removed before ours", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				old["metadata"].(map[string]any)["finalizers"] = []string{sriovnetworkv1.NETATTDEFFINALIZERNAME, "foregroundDeletion"}
				current["metadata"].(map[string]any)["finalizers"] = []string{sriovnetworkv1.NETATTDEFFINALIZERNAME}
			}, true},
			{"foreign finalizer removed after ours", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				old["metadata"].(map[string]any)["finalizers"] = []string{"example.com/other"}
				current["metadata"].(map[string]any)["finalizers"] = []string{}
			}, true},
			{"GC finalizer removed after ours", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				old["metadata"].(map[string]any)["finalizers"] = []string{"foregroundDeletion"}
				current["metadata"].(map[string]any)["finalizers"] = []string{}
			}, true},
			{"shorter replacement", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["metadata"].(map[string]any)["finalizers"] = []string{"example.com/new"}
			}, false},
			{"empty old finalizers", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				old["metadata"].(map[string]any)["finalizers"] = []string{}
				current["metadata"].(map[string]any)["finalizers"] = []string{}
			}, false},
			{"duplicate introduced in shorter list", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				old["metadata"].(map[string]any)["finalizers"] = []string{sriovnetworkv1.NETATTDEFFINALIZERNAME, "example.com/other", "foregroundDeletion"}
				current["metadata"].(map[string]any)["finalizers"] = []string{"example.com/other", "example.com/other"}
			}, false},
			{"unrelated finalizer added", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["metadata"].(map[string]any)["finalizers"] = []string{"example.com/other", "example.com/new"}
			}, false},
			{"missing controller finalizer without removal", func(old, _ map[string]any, _ *admissionv1.AdmissionRequest) {
				old["metadata"].(map[string]any)["finalizers"] = []string{"example.com/other"}
			}, false},
			{"changed identity", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["metadata"].(map[string]any)["uid"] = "another-uid"
			}, false},
			{"missing identity", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				delete(old["metadata"].(map[string]any), "uid")
				delete(current["metadata"].(map[string]any), "uid")
			}, false},
			{"changed target namespace", func(_, current map[string]any, _ *admissionv1.AdmissionRequest) {
				current["spec"].(map[string]any)["networkNamespace"] = "other"
			}, false},
			{"unauthorized namespace", func(old, current map[string]any, _ *admissionv1.AdmissionRequest) {
				for _, object := range []map[string]any{old, current} {
					object["metadata"].(map[string]any)["namespace"] = vars.Namespace + "-tenant"
					object["spec"].(map[string]any)["networkNamespace"] = "other"
				}
			}, false},
			{"create", func(_, _ map[string]any, request *admissionv1.AdmissionRequest) {
				request.Operation = admissionv1.Create
			}, false},
			{"missing old object", nil, false},
			{"malformed old object", nil, false},
			{"null old object", nil, false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				newObject := func() map[string]any {
					return map[string]any{
						"apiVersion": "sriovnetwork.openshift.io/v1", "kind": kind,
						"metadata": map[string]any{
							"name": "legacy", "namespace": vars.Namespace, "uid": "network-uid",
							"deletionTimestamp": "2026-10-19T19:00:00Z",
							"finalizers":        []string{sriovnetworkv1.NETATTDEFFINALIZERNAME, "example.com/other"},
						},
						"spec": map[string]any{"ipam": "null"},
					}
				}
				old, current := newObject(), newObject()
				current["metadata"].(map[string]any)["finalizers"] = []string{"example.com/other"}
				request := &admissionv1.AdmissionRequest{
					Kind:      metav1.GroupVersionKind{Group: "sriovnetwork.openshift.io", Version: "v1", Kind: kind},
					Operation: admissionv1.Update,
				}
				if tc.change != nil {
					tc.change(old, current, request)
				}
				marshal := func(object any) []byte {
					raw, err := json.Marshal(object)
					if err != nil {
						t.Fatal(err)
					}
					return raw
				}
				request.Object.Raw = marshal(current)
				request.OldObject.Raw = marshal(old)
				switch tc.name {
				case "missing old object":
					request.OldObject.Raw = nil
				case "malformed old object":
					request.OldObject.Raw = []byte("{")
				case "null old object":
					request.OldObject.Raw = []byte("null")
				}
				response := ValidateCustomResource(admissionv1.AdmissionReview{Request: request})
				if response.Allowed != tc.allowed {
					t.Fatalf("allowed = %v, want %v: %+v", response.Allowed, tc.allowed, response.Result)
				}
			})
		}
	}
}

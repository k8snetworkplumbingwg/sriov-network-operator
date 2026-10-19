package webhook

import (
	"encoding/json"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// isNetworkFinalizerCleanup lets controllers finish deleting legacy objects whose
// unchanged specs no longer pass field validation. Only removal of existing
// finalizers is allowed, including finalizers owned by other controllers.
func isNetworkFinalizerCleanup[S any](request *admissionv1.AdmissionRequest, network metav1.Object, spec S) bool {
	if request.Operation != admissionv1.Update {
		return false
	}
	var old struct {
		metav1.ObjectMeta `json:"metadata"`
		Spec              S `json:"spec"`
	}
	if err := json.Unmarshal(request.OldObject.Raw, &old); err != nil {
		return false
	}
	deletion := network.GetDeletionTimestamp()
	if deletion == nil || deletion.IsZero() || old.DeletionTimestamp == nil ||
		!deletion.Equal(old.DeletionTimestamp) ||
		old.Name == "" || old.UID == "" ||
		old.Name != network.GetName() || old.Namespace != network.GetNamespace() ||
		old.UID != network.GetUID() || !equality.Semantic.DeepEqual(old.Spec, spec) {
		return false
	}
	finalizers := network.GetFinalizers()
	if len(finalizers) >= len(old.Finalizers) {
		return false
	}
	counts := make(map[string]int, len(old.Finalizers))
	for _, finalizer := range old.Finalizers {
		counts[finalizer]++
	}
	for _, finalizer := range finalizers {
		if counts[finalizer] == 0 {
			return false
		}
		counts[finalizer]--
	}
	log.Log.V(2).Info("allowing legacy network finalizer cleanup",
		"kind", request.Kind.Kind, "name", network.GetName(), "namespace", network.GetNamespace())
	return true
}

package pod

import (
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAssertContainerReady(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*corev1.Pod)
		wantFailure bool
	}{
		{name: "ready target"},
		{name: "missing spec", mutate: func(p *corev1.Pod) { p.Spec.Containers = nil }, wantFailure: true},
		{name: "missing status", mutate: func(p *corev1.Pod) { p.Status.ContainerStatuses = nil }, wantFailure: true},
		{name: "wrong ready container", mutate: func(p *corev1.Pod) { p.Status.ContainerStatuses[0].Name = "other" }, wantFailure: true},
		{name: "target not ready", mutate: func(p *corev1.Pod) { p.Status.ContainerStatuses[0].Ready = false }, wantFailure: true},
		{name: "not running", mutate: func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }, wantFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "operand", Namespace: "operator"},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "target"}}},
				Status: corev1.PodStatus{
					Phase:             corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{{Name: "target", Ready: true}},
				},
			}
			if tt.mutate != nil {
				tt.mutate(&p)
			}
			failed := false
			g := gomega.NewGomega(func(string, ...int) { failed = true })
			AssertContainerReady(g, p, "target")
			if failed != tt.wantFailure {
				t.Fatalf("assertion failure = %v, want %v", failed, tt.wantFailure)
			}
		})
	}
}

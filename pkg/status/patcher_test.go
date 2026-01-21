/*
Copyright 2021.

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

package status

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStatus(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Status Package Suite")
}

var _ = Describe("Patcher Condition Methods", func() {
	var patcher *Patcher

	BeforeEach(func() {
		patcher = &Patcher{}
	})

	Describe("SetCondition", func() {
		It("should set a new condition", func() {
			conditions := []metav1.Condition{}
			patcher.SetCondition(&conditions, "Ready", metav1.ConditionTrue, "TestReason", "Test message", 1)

			Expect(conditions).To(HaveLen(1))
			Expect(conditions[0].Type).To(Equal("Ready"))
			Expect(conditions[0].Status).To(Equal(metav1.ConditionTrue))
			Expect(conditions[0].Reason).To(Equal("TestReason"))
			Expect(conditions[0].Message).To(Equal("Test message"))
			Expect(conditions[0].ObservedGeneration).To(Equal(int64(1)))
		})

		It("should update an existing condition", func() {
			conditions := []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionFalse,
					Reason:             "OldReason",
					Message:            "Old message",
					ObservedGeneration: 1,
				},
			}

			patcher.SetCondition(&conditions, "Ready", metav1.ConditionTrue, "NewReason", "New message", 2)

			Expect(conditions).To(HaveLen(1))
			Expect(conditions[0].Type).To(Equal("Ready"))
			Expect(conditions[0].Status).To(Equal(metav1.ConditionTrue))
			Expect(conditions[0].Reason).To(Equal("NewReason"))
			Expect(conditions[0].Message).To(Equal("New message"))
			Expect(conditions[0].ObservedGeneration).To(Equal(int64(2)))
		})
	})

	Describe("IsConditionTrue", func() {
		It("should return true when condition exists and is True", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue},
			}
			Expect(patcher.IsConditionTrue(conditions, "Ready")).To(BeTrue())
		})

		It("should return false when condition exists but is not True", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionFalse},
			}
			Expect(patcher.IsConditionTrue(conditions, "Ready")).To(BeFalse())
		})

		It("should return false when condition does not exist", func() {
			conditions := []metav1.Condition{}
			Expect(patcher.IsConditionTrue(conditions, "Ready")).To(BeFalse())
		})
	})

	Describe("IsConditionFalse", func() {
		It("should return true when condition exists and is False", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionFalse},
			}
			Expect(patcher.IsConditionFalse(conditions, "Ready")).To(BeTrue())
		})

		It("should return false when condition exists but is not False", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue},
			}
			Expect(patcher.IsConditionFalse(conditions, "Ready")).To(BeFalse())
		})
	})

	Describe("IsConditionUnknown", func() {
		It("should return true when condition exists and is Unknown", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionUnknown},
			}
			Expect(patcher.IsConditionUnknown(conditions, "Ready")).To(BeTrue())
		})
	})

	Describe("FindCondition", func() {
		It("should find an existing condition", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue},
			}
			condition := patcher.FindCondition(conditions, "Ready")
			Expect(condition).ToNot(BeNil())
			Expect(condition.Type).To(Equal("Ready"))
		})

		It("should return nil when condition does not exist", func() {
			conditions := []metav1.Condition{}
			condition := patcher.FindCondition(conditions, "Ready")
			Expect(condition).To(BeNil())
		})
	})

	Describe("RemoveCondition", func() {
		It("should remove an existing condition", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue},
				{Type: "Degraded", Status: metav1.ConditionFalse},
			}
			patcher.RemoveCondition(&conditions, "Ready")
			Expect(conditions).To(HaveLen(1))
			Expect(conditions[0].Type).To(Equal("Degraded"))
		})

		It("should do nothing when condition does not exist", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue},
			}
			patcher.RemoveCondition(&conditions, "NonExistent")
			Expect(conditions).To(HaveLen(1))
		})
	})

	Describe("HasConditionChanged", func() {
		It("should return true when condition does not exist", func() {
			conditions := []metav1.Condition{}
			Expect(patcher.HasConditionChanged(conditions, "Ready", metav1.ConditionTrue, "Reason", "Message")).To(BeTrue())
		})

		It("should return true when status changed", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Reason", Message: "Message"},
			}
			Expect(patcher.HasConditionChanged(conditions, "Ready", metav1.ConditionTrue, "Reason", "Message")).To(BeTrue())
		})

		It("should return true when reason changed", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "OldReason", Message: "Message"},
			}
			Expect(patcher.HasConditionChanged(conditions, "Ready", metav1.ConditionTrue, "NewReason", "Message")).To(BeTrue())
		})

		It("should return true when message changed", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Reason", Message: "Old"},
			}
			Expect(patcher.HasConditionChanged(conditions, "Ready", metav1.ConditionTrue, "Reason", "New")).To(BeTrue())
		})

		It("should return false when nothing changed", func() {
			conditions := []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Reason", Message: "Message"},
			}
			Expect(patcher.HasConditionChanged(conditions, "Ready", metav1.ConditionTrue, "Reason", "Message")).To(BeFalse())
		})
	})
})

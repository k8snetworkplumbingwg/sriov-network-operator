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

package v1_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

var _ = Describe("SetConfigurationConditions", func() {
	var nodeState *v1.SriovNetworkNodeState

	BeforeEach(func() {
		nodeState = &v1.SriovNetworkNodeState{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-node",
				Namespace:  "test-ns",
				Generation: 5,
			},
		}
	})

	Context("when SyncStatus is InProgress", func() {
		It("should set Progressing=True, Ready=False, Degraded=False", func() {
			nodeState.SetConfigurationConditions(consts.SyncStatusInProgress, "")

			progressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionProgressing)
			Expect(progressing).ToNot(BeNil())
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(v1.ReasonApplyingConfiguration))
			Expect(progressing.ObservedGeneration).To(Equal(int64(5)))

			ready := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionReady)
			Expect(ready).ToNot(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(v1.ReasonNotReady))

			degraded := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDegraded)
			Expect(degraded).ToNot(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionFalse))
			Expect(degraded.Reason).To(Equal(v1.ReasonNotDegraded))
		})

		It("should set Degraded=True when retrying after previous error", func() {
			nodeState.SetConfigurationConditions(consts.SyncStatusInProgress, "previous error message")

			degraded := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDegraded)
			Expect(degraded).ToNot(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal(v1.ReasonConfigurationFailed))
			Expect(degraded.Message).To(ContainSubstring("Retrying after previous failure"))
		})
	})

	Context("when SyncStatus is Succeeded", func() {
		It("should set Progressing=False, Ready=True, Degraded=False", func() {
			nodeState.Generation = 10
			nodeState.SetConfigurationConditions(consts.SyncStatusSucceeded, "")

			progressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionProgressing)
			Expect(progressing).ToNot(BeNil())
			Expect(progressing.Status).To(Equal(metav1.ConditionFalse))
			Expect(progressing.Reason).To(Equal(v1.ReasonNotProgressing))

			ready := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionReady)
			Expect(ready).ToNot(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			Expect(ready.Reason).To(Equal(v1.ReasonNodeReady))

			degraded := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDegraded)
			Expect(degraded).ToNot(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionFalse))
			Expect(degraded.Reason).To(Equal(v1.ReasonNotDegraded))
		})
	})

	Context("when SyncStatus is Failed", func() {
		It("should set Progressing=False, Ready=False, Degraded=True with error message", func() {
			nodeState.Generation = 7
			errorMsg := "driver load failed"
			nodeState.SetConfigurationConditions(consts.SyncStatusFailed, errorMsg)

			progressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionProgressing)
			Expect(progressing).ToNot(BeNil())
			Expect(progressing.Status).To(Equal(metav1.ConditionFalse))

			ready := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionReady)
			Expect(ready).ToNot(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))

			degraded := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDegraded)
			Expect(degraded).ToNot(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal(v1.ReasonConfigurationFailed))
			Expect(degraded.Message).To(Equal("Node configuration failed: " + errorMsg))
		})
	})
})

var _ = Describe("SetDrainConditions", func() {
	var nodeState *v1.SriovNetworkNodeState

	BeforeEach(func() {
		nodeState = &v1.SriovNetworkNodeState{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-node",
				Namespace:  "test-ns",
				Generation: 3,
			},
		}
	})

	Context("when DrainState is Idle", func() {
		It("should set all drain conditions to idle state", func() {
			nodeState.SetDrainConditions(v1.DrainStateIdle, "")

			drainProgressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainProgressing)
			Expect(drainProgressing).ToNot(BeNil())
			Expect(drainProgressing.Status).To(Equal(metav1.ConditionFalse))
			Expect(drainProgressing.Reason).To(Equal(v1.ReasonNotProgressing))

			drainDegraded := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainDegraded)
			Expect(drainDegraded).ToNot(BeNil())
			Expect(drainDegraded.Status).To(Equal(metav1.ConditionFalse))
			Expect(drainDegraded.Reason).To(Equal(v1.ReasonNotDegraded))

			drainComplete := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainComplete)
			Expect(drainComplete).ToNot(BeNil())
			Expect(drainComplete.Status).To(Equal(metav1.ConditionFalse))
			Expect(drainComplete.Reason).To(Equal(v1.ReasonDrainNotNeeded))
		})
	})

	Context("when DrainState is Draining", func() {
		It("should set DrainProgressing=True, DrainDegraded=False, DrainComplete=False", func() {
			nodeState.Generation = 4
			nodeState.SetDrainConditions(v1.DrainStateDraining, "")

			drainProgressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainProgressing)
			Expect(drainProgressing).ToNot(BeNil())
			Expect(drainProgressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(drainProgressing.Reason).To(Equal(v1.ReasonDrainingNode))

			drainDegraded := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainDegraded)
			Expect(drainDegraded).ToNot(BeNil())
			Expect(drainDegraded.Status).To(Equal(metav1.ConditionFalse))

			drainComplete := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainComplete)
			Expect(drainComplete).ToNot(BeNil())
			Expect(drainComplete.Status).To(Equal(metav1.ConditionFalse))
			Expect(drainComplete.Reason).To(Equal(v1.ReasonDrainPending))
		})
	})

	Context("when DrainState is DrainingWithErrors", func() {
		It("should set DrainProgressing=True, DrainDegraded=True with error message", func() {
			nodeState.Generation = 6
			errorMsg := "Cannot evict pod as it would violate the pod's disruption budget"
			nodeState.SetDrainConditions(v1.DrainStateDrainingWithErrors, errorMsg)

			drainProgressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainProgressing)
			Expect(drainProgressing).ToNot(BeNil())
			Expect(drainProgressing.Status).To(Equal(metav1.ConditionTrue))

			drainDegraded := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainDegraded)
			Expect(drainDegraded).ToNot(BeNil())
			Expect(drainDegraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(drainDegraded.Reason).To(Equal(v1.ReasonDrainFailed))
			Expect(drainDegraded.Message).To(Equal("Node drain encountered errors: " + errorMsg))

			drainComplete := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainComplete)
			Expect(drainComplete).ToNot(BeNil())
			Expect(drainComplete.Status).To(Equal(metav1.ConditionFalse))
		})
	})

	Context("when DrainState is Complete", func() {
		It("should set DrainProgressing=False, DrainDegraded=False, DrainComplete=True", func() {
			nodeState.Generation = 8
			nodeState.SetDrainConditions(v1.DrainStateComplete, "")

			drainProgressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainProgressing)
			Expect(drainProgressing).ToNot(BeNil())
			Expect(drainProgressing.Status).To(Equal(metav1.ConditionFalse))
			Expect(drainProgressing.Reason).To(Equal(v1.ReasonNotProgressing))

			drainDegraded := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainDegraded)
			Expect(drainDegraded).ToNot(BeNil())
			Expect(drainDegraded.Status).To(Equal(metav1.ConditionFalse))

			drainComplete := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainComplete)
			Expect(drainComplete).ToNot(BeNil())
			Expect(drainComplete.Status).To(Equal(metav1.ConditionTrue))
			Expect(drainComplete.Reason).To(Equal(v1.ReasonDrainCompleted))
		})
	})
})

var _ = Describe("ConditionsEqual", func() {
	DescribeTable("comparing conditions",
		func(a, b []metav1.Condition, expected bool) {
			result := v1.ConditionsEqual(a, b)
			Expect(result).To(Equal(expected))
		},
		Entry("both empty",
			[]metav1.Condition{},
			[]metav1.Condition{},
			true,
		),
		Entry("different length",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue},
			},
			[]metav1.Condition{},
			false,
		),
		Entry("same conditions",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
			},
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
			},
			true,
		),
		Entry("different status",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
			},
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionFalse, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
			},
			false,
		),
		Entry("different reason",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
			},
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNotReady, Message: "ready", ObservedGeneration: 1},
			},
			false,
		),
		Entry("different message",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
			},
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "not ready", ObservedGeneration: 1},
			},
			false,
		),
		Entry("different observedGeneration",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
			},
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 2},
			},
			false,
		),
		Entry("different LastTransitionTime should still be equal",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1, LastTransitionTime: metav1.Now()},
			},
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1, LastTransitionTime: metav1.Now()},
			},
			true,
		),
		Entry("multiple conditions same",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
				{Type: v1.ConditionProgressing, Status: metav1.ConditionFalse, Reason: v1.ReasonNotProgressing, Message: "not progressing", ObservedGeneration: 1},
			},
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
				{Type: v1.ConditionProgressing, Status: metav1.ConditionFalse, Reason: v1.ReasonNotProgressing, Message: "not progressing", ObservedGeneration: 1},
			},
			true,
		),
		Entry("condition type not found in second slice",
			[]metav1.Condition{
				{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonNodeReady, Message: "ready", ObservedGeneration: 1},
			},
			[]metav1.Condition{
				{Type: v1.ConditionProgressing, Status: metav1.ConditionFalse, Reason: v1.ReasonNotProgressing, Message: "not progressing", ObservedGeneration: 1},
			},
			false,
		),
	)
})

var _ = Describe("Conditions isolation", func() {
	It("should preserve drain conditions when setting configuration conditions", func() {
		nodeState := &v1.SriovNetworkNodeState{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-node",
				Namespace:  "test-ns",
				Generation: 5,
			},
		}

		// First set drain conditions
		nodeState.SetDrainConditions(v1.DrainStateDraining, "")

		// Verify drain conditions are set
		drainProgressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainProgressing)
		Expect(drainProgressing).ToNot(BeNil())
		Expect(drainProgressing.Status).To(Equal(metav1.ConditionTrue))

		// Now set configuration conditions
		nodeState.SetConfigurationConditions(consts.SyncStatusInProgress, "")

		// Verify configuration conditions are set
		progressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionProgressing)
		Expect(progressing).ToNot(BeNil())
		Expect(progressing.Status).To(Equal(metav1.ConditionTrue))

		// Verify drain conditions are still intact
		drainProgressing = meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainProgressing)
		Expect(drainProgressing).ToNot(BeNil())
		Expect(drainProgressing.Status).To(Equal(metav1.ConditionTrue))
	})

	It("should preserve configuration conditions when setting drain conditions", func() {
		nodeState := &v1.SriovNetworkNodeState{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-node",
				Namespace:  "test-ns",
				Generation: 5,
			},
		}

		// First set configuration conditions
		nodeState.SetConfigurationConditions(consts.SyncStatusSucceeded, "")

		// Verify configuration conditions are set
		ready := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionReady)
		Expect(ready).ToNot(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))

		// Now set drain conditions
		nodeState.SetDrainConditions(v1.DrainStateDraining, "")

		// Verify drain conditions are set
		drainProgressing := meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionDrainProgressing)
		Expect(drainProgressing).ToNot(BeNil())
		Expect(drainProgressing.Status).To(Equal(metav1.ConditionTrue))

		// Verify configuration conditions are still intact
		ready = meta.FindStatusCondition(nodeState.Status.Conditions, v1.ConditionReady)
		Expect(ready).ToNot(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
	})
})

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
)

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

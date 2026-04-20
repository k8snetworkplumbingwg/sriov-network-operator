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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// MaxRetries is the maximum number of retries for status updates on conflict
	MaxRetries = 3
)

// Interface provides methods for updating resource status with retry logic and event emission
//
//go:generate ../../bin/mockgen -destination mock/mock_patcher.go -source patcher.go
type Interface interface {
	// UpdateStatus updates the status of a resource with retry on conflict
	UpdateStatus(ctx context.Context, obj client.Object, updateFunc func() error) error

	// PatchStatus patches the status of a resource with retry on conflict
	PatchStatus(ctx context.Context, obj client.Object, patch client.Patch) error

	// UpdateStatusWithEvents updates status and emits events for condition transitions
	UpdateStatusWithEvents(ctx context.Context, obj client.Object, oldConditions, newConditions []metav1.Condition, updateFunc func() error) error
}

// Patcher is the production implementation of Interface
type Patcher struct {
	client   client.Client
	recorder events.EventRecorder
	scheme   *runtime.Scheme
}

// NewPatcher creates a new Patcher instance
func NewPatcher(client client.Client, recorder events.EventRecorder, scheme *runtime.Scheme) Interface {
	return &Patcher{
		client:   client,
		recorder: recorder,
		scheme:   scheme,
	}
}

// UpdateStatus updates the status of a resource with retry on conflict
func (p *Patcher) UpdateStatus(ctx context.Context, obj client.Object, updateFunc func() error) error {
	var lastErr error

	for i := 0; i < MaxRetries; i++ {
		// Apply the update function
		if err := updateFunc(); err != nil {
			return fmt.Errorf("failed to apply status update: %w", err)
		}

		// Try to update the status
		if err := p.client.Status().Update(ctx, obj); err != nil {
			if errors.IsConflict(err) {
				// Conflict, retry after fetching latest version
				lastErr = err
				if err := p.client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
					return fmt.Errorf("failed to fetch latest version for retry: %w", err)
				}
				continue
			}
			return fmt.Errorf("failed to update status: %w", err)
		}

		// Success
		return nil
	}

	return fmt.Errorf("failed to update status after %d retries: %w", MaxRetries, lastErr)
}

// PatchStatus patches the status of a resource with retry on conflict
func (p *Patcher) PatchStatus(ctx context.Context, obj client.Object, patch client.Patch) error {
	var lastErr error

	for i := 0; i < MaxRetries; i++ {
		if err := p.client.Status().Patch(ctx, obj, patch); err != nil {
			if errors.IsConflict(err) {
				// Conflict, retry after fetching latest version
				lastErr = err
				if err := p.client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
					return fmt.Errorf("failed to fetch latest version for retry: %w", err)
				}
				continue
			}
			return fmt.Errorf("failed to patch status: %w", err)
		}

		// Success
		return nil
	}

	return fmt.Errorf("failed to patch status after %d retries: %w", MaxRetries, lastErr)
}

// UpdateStatusWithEvents updates status using Patch and emits events for condition transitions.
// Using Patch instead of Update ensures that only the modified fields are updated,
// preventing race conditions where concurrent updates could overwrite each other's changes.
func (p *Patcher) UpdateStatusWithEvents(ctx context.Context, obj client.Object, oldConditions, newConditions []metav1.Condition, updateFunc func() error) error {
	var lastErr error

	for i := 0; i < MaxRetries; i++ {
		// Create a deep copy before modifications to use as patch base
		baseCopy := obj.DeepCopyObject().(client.Object)

		// Apply the update function
		if err := updateFunc(); err != nil {
			return fmt.Errorf("failed to apply status update: %w", err)
		}

		// Use Patch instead of Update to avoid overwriting concurrent changes
		if err := p.client.Status().Patch(ctx, obj, client.MergeFrom(baseCopy)); err != nil {
			if errors.IsConflict(err) {
				// Conflict, retry after fetching latest version
				lastErr = err
				if err := p.client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
					return fmt.Errorf("failed to fetch latest version for retry: %w", err)
				}
				continue
			}
			return fmt.Errorf("failed to patch status: %w", err)
		}

		// Success - emit events for condition transitions
		if p.recorder != nil {
			transitions := DetectTransitions(oldConditions, newConditions)
			for _, transition := range transitions {
				if transition.Type != TransitionUnchanged {
					p.recorder.Eventf(obj, nil, transition.EventType(), transition.EventReason(), "StatusChange", transition.Message)
				}
			}
		}

		return nil
	}

	return fmt.Errorf("failed to patch status after %d retries: %w", MaxRetries, lastErr)
}

// Condition helper functions

// SetCondition sets a condition with the given type, status, reason, and message
func SetCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, generation int64) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	}
	meta.SetStatusCondition(conditions, condition)
}

// IsConditionTrue checks if a condition exists and has status True
func IsConditionTrue(conditions []metav1.Condition, conditionType string) bool {
	condition := meta.FindStatusCondition(conditions, conditionType)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

// IsConditionFalse checks if a condition exists and has status False
func IsConditionFalse(conditions []metav1.Condition, conditionType string) bool {
	condition := meta.FindStatusCondition(conditions, conditionType)
	return condition != nil && condition.Status == metav1.ConditionFalse
}

// IsConditionUnknown checks if a condition exists and has status Unknown
func IsConditionUnknown(conditions []metav1.Condition, conditionType string) bool {
	condition := meta.FindStatusCondition(conditions, conditionType)
	return condition != nil && condition.Status == metav1.ConditionUnknown
}

// FindCondition returns the condition with the given type, or nil if not found
func FindCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(conditions, conditionType)
}

// RemoveCondition removes the condition with the given type
func RemoveCondition(conditions *[]metav1.Condition, conditionType string) {
	meta.RemoveStatusCondition(conditions, conditionType)
}

// HasConditionChanged checks if the new condition differs from the existing one
// Returns true if the condition doesn't exist or if status, reason, or message differ
func HasConditionChanged(conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string) bool {
	existing := meta.FindStatusCondition(conditions, conditionType)
	if existing == nil {
		return true
	}
	return existing.Status != status || existing.Reason != reason || existing.Message != message
}

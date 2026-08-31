// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"slices"
	"time"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ocirepo"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

const (
	CloudProfileAppliedConditionType string = "CloudProfileApplied"
)

// OCISourceFactory defines an interface for creating OCI sources.
type OCISourceFactory interface {
	Create(params ocirepo.Params, parallel int64, log logr.Logger, capabilityKeys []string) (ossync.Source, error)
}

type RegistryClient interface {
	GetTags(ctx context.Context, registry, repository string) (map[string]time.Time, error)
}

type Reconciler struct {
	client.Client
	OCISourceFactory     OCISourceFactory
	RegistryProviderFunc func(registry string) (RegistryClient, error)
	EnableCapabilities   bool
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	var mcp v1alpha1.ManagedCloudProfile
	if err := r.Get(ctx, req.NamespacedName, &mcp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.reconcileCloudProfile(ctx, log, &mcp); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileGarbageCollection(ctx, &mcp); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("reconciled ManagedCloudProfile")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *Reconciler) patchStatusAndCondition(ctx context.Context, mcp *v1alpha1.ManagedCloudProfile, status v1alpha1.ReconcileStatus, cond metav1.Condition) error {
	original := mcp.DeepCopy()
	mcp.Status.Status = status
	if cond.Type != "" {
		mcp.Status.Conditions = applyCondition(mcp.Status.Conditions, cond)
	}
	return r.Status().Patch(ctx, mcp, client.MergeFrom(original))
}

func applyCondition(conditions []metav1.Condition, cond metav1.Condition) []metav1.Condition {
	idx := slices.IndexFunc(conditions, func(c metav1.Condition) bool {
		return c.Type == cond.Type
	})
	if idx == -1 {
		idx = len(conditions)
		conditions = append(conditions, metav1.Condition{})
	}
	lastTransition := conditions[idx].LastTransitionTime
	if conditions[idx].Status != cond.Status {
		lastTransition = metav1.Now()
	}
	conditions[idx] = metav1.Condition{
		Type:               cond.Type,
		Status:             cond.Status,
		ObservedGeneration: cond.ObservedGeneration,
		LastTransitionTime: lastTransition,
		Reason:             cond.Reason,
		Message:            cond.Message,
	}
	return conditions
}

func CloudProfileSpecToGardener(spec *v1alpha1.CloudProfileSpec) gardenerv1beta1.CloudProfileSpec {
	cpu := spec.DeepCopy()
	return gardenerv1beta1.CloudProfileSpec{
		CABundle:            cpu.CABundle,
		Kubernetes:          cpu.Kubernetes,
		MachineImages:       cpu.MachineImages,
		MachineTypes:        cpu.MachineTypes,
		ProviderConfig:      cpu.ProviderConfig,
		Regions:             cpu.Regions,
		SeedSelector:        cpu.SeedSelector,
		Type:                cpu.Type,
		VolumeTypes:         cpu.VolumeTypes,
		Bastion:             cpu.Bastion,
		Limits:              cpu.Limits,
		MachineCapabilities: cpu.MachineCapabilities,
	}
}

// SetupWithManager attaches the controller to the given manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.OCISourceFactory == nil {
		r.OCISourceFactory = &DefaultOCISourceFactory{}
	}
	if r.RegistryProviderFunc == nil {
		r.RegistryProviderFunc = r.getRegistryProvider
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ManagedCloudProfile{}).
		Owns(&gardenerv1beta1.CloudProfile{}).
		Complete(r)
}

// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

const (
	CloudProfileAppliedConditionType string = "CloudProfileApplied"
)

type Reconciler struct {
	client.Client
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	var mcp v1alpha1.ManagedCloudProfile
	if err := r.Get(ctx, req.NamespacedName, &mcp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var cloudProfile gardenerv1beta1.CloudProfile
	cloudProfile.Name = mcp.Name

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, &cloudProfile, func() error {
		err := controllerutil.SetControllerReference(&mcp, &cloudProfile, r.Scheme())
		if err != nil {
			return err
		}
		cloudProfile.Spec = CloudProfileSpecToGardener(&mcp.Spec.CloudProfile)
		errs := make([]error, 0)
		for _, updates := range mcp.Spec.MachineImageUpdates {
			errs = append(errs, r.updateMachineImages(ctx, log, updates, &cloudProfile.Spec))
		}
		gardenerv1beta1.SetObjectDefaults_CloudProfile(&cloudProfile)
		return errors.Join(errs...)
	})
	if err != nil {
		if err := r.patchStatusAndCondition(ctx, &mcp, v1alpha1.FailedReconcileStatus, metav1.Condition{
			Type:               CloudProfileAppliedConditionType,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: mcp.Generation,
			Reason:             "ApplyFailed",
			Message:            fmt.Sprintf("Failed to apply CloudProfile: %s", err),
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch ManagedCloudProfile status: %w", err)
		}
		if apierrors.IsInvalid(err) {
			log.Error(err, "tried to apply invalid CloudProfile")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to create or patch CloudProfile: %w", err)
	}
	log.Info("applied cloud profile", "op", op)

	if err := r.patchStatusAndCondition(ctx, &mcp, v1alpha1.SucceededReconcileStatus, metav1.Condition{
		Type:               CloudProfileAppliedConditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: mcp.Generation,
		Reason:             "Applied",
		Message:            "Generated CloudProfile applied successfully",
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch ManagedCloudProfile status: %w", err)
	}
	log.Info("reconciled ManagedCloudProfile")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *Reconciler) updateMachineImages(ctx context.Context, log logr.Logger, update v1alpha1.MachineImageUpdate, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	var source cloudprofilesync.Source
	switch {
	case update.Source.OCI != nil:
		password, err := r.getCredential(ctx, update.Source.OCI.Password)
		if err != nil {
			return err
		}
		oci, err := cloudprofilesync.NewOCI(cloudprofilesync.OCIParams{
			Registry:   update.Source.OCI.Registry,
			Repository: update.Source.OCI.Repository,
			Username:   update.Source.OCI.Username,
			Password:   string(password),
			Parallel:   1,
		}, update.Source.OCI.Insecure)
		if err != nil {
			return fmt.Errorf("failed to initialize oci source: %w", err)
		}
		source = oci
	default:
		return errors.New("no machine images source configured")
	}

	var provider cloudprofilesync.Provider
	if update.Provider.IroncoreMetal != nil {
		provider = &cloudprofilesync.IroncoreProvider{
			Registry:   update.Provider.IroncoreMetal.Registry,
			Repository: update.Provider.IroncoreMetal.Repository,
			ImageName:  update.ImageName,
		}
	}
	imageUpdater := cloudprofilesync.ImageUpdater{
		Log:       log,
		Source:    source,
		Provider:  provider,
		ImageName: update.ImageName,
	}
	if err := imageUpdater.Update(ctx, cpSpec); err != nil {
		return fmt.Errorf("updating machine images failed: %w", err)
	}
	return nil
}

func (r *Reconciler) getCredential(ctx context.Context, ref v1alpha1.SecretReference) ([]byte, error) {
	if ref.Name == "" {
		return nil, nil
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}
	data, ok := secret.Data[ref.Key]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not have key %s", ref.Namespace, ref.Name, ref.Key)
	}
	return data, nil
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
	conditions[idx] = metav1.Condition{
		Type:               cond.Type,
		Status:             cond.Status,
		ObservedGeneration: cond.ObservedGeneration,
		LastTransitionTime: metav1.Now(),
		Reason:             cond.Reason,
		Message:            cond.Message,
	}
	return conditions
}

func CloudProfileSpecToGardener(spec *v1alpha1.CloudProfileSpec) gardenerv1beta1.CloudProfileSpec {
	cpy := spec.DeepCopy()
	return gardenerv1beta1.CloudProfileSpec{
		CABundle:            cpy.CABundle,
		Kubernetes:          cpy.Kubernetes,
		MachineImages:       cpy.MachineImages,
		MachineTypes:        cpy.MachineTypes,
		ProviderConfig:      cpy.ProviderConfig,
		Regions:             cpy.Regions,
		SeedSelector:        cpy.SeedSelector,
		Type:                cpy.Type,
		VolumeTypes:         cpy.VolumeTypes,
		Bastion:             cpy.Bastion,
		Limits:              cpy.Limits,
		MachineCapabilities: cpy.MachineCapabilities,
	}
}

// SetupWithManager attaches the controller to the given manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ManagedCloudProfile{}).
		Owns(&gardenerv1beta1.CloudProfile{}).
		Complete(r)
}

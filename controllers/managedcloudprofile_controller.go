// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	providercfg "github.com/ironcore-dev/gardener-extension-provider-ironcore-metal/pkg/apis/metal/v1alpha1"
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

// OCIFactory provides a hook for constructing OCI sources. It defaults to
// cloudprofilesync.NewOCI but can be overridden in tests to simulate errors
var OCIFactory = func(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error) {
	return cloudprofilesync.NewOCI(params, insecure)
}

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
			// only call updater when an explicit source is provided
			if updates.Source.OCI == nil {
				continue
			}
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
	if op != controllerutil.OperationResultNone {
		if err := r.patchStatusAndCondition(ctx, &mcp, v1alpha1.SucceededReconcileStatus, metav1.Condition{
			Type:               CloudProfileAppliedConditionType,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: mcp.Generation,
			Reason:             "Applied",
			Message:            "Generated CloudProfile applied successfully",
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch ManagedCloudProfile status: %w", err)
		}
	}
	log.Info("reconciled ManagedCloudProfile")
	for _, updates := range mcp.Spec.MachineImageUpdates {
		if updates.GarbageCollection == nil || !updates.GarbageCollection.Enabled {
			continue
		}
		var source cloudprofilesync.Source
		switch {
		case updates.Source.OCI != nil:
			password, err := r.getCredential(ctx, updates.Source.OCI.Password)
			if err != nil {
				return ctrl.Result{}, err
			}
			src, err := OCIFactory(cloudprofilesync.OCIParams{
				Registry:   updates.Source.OCI.Registry,
				Repository: updates.Source.OCI.Repository,
				Username:   updates.Source.OCI.Username,
				Password:   string(password),
				Parallel:   1,
			}, updates.Source.OCI.Insecure)
			if err != nil {
				if patchErr := r.patchStatusAndCondition(ctx, &mcp, v1alpha1.FailedReconcileStatus, metav1.Condition{
					Type:               CloudProfileAppliedConditionType,
					Status:             metav1.ConditionFalse,
					ObservedGeneration: mcp.Generation,
					Reason:             "GarbageCollectionFailed",
					Message:            fmt.Sprintf("failed to initialize OCI source for garbage collection: %s", err),
				}); patchErr != nil {
					return ctrl.Result{}, fmt.Errorf("failed to patch ManagedCloudProfile status: %w (original error: %w)", patchErr, err)
				}
				return ctrl.Result{}, fmt.Errorf("failed to initialize OCI source for garbage collection: %w", err)
			}
			source = src
		default:
			continue
		}

		versions, err := source.GetVersions(ctx)
		if err != nil {
			if patchErr := r.patchStatusAndCondition(ctx, &mcp, v1alpha1.FailedReconcileStatus, metav1.Condition{
				Type:               CloudProfileAppliedConditionType,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: mcp.Generation,
				Reason:             "GarbageCollectionFailed",
				Message:            fmt.Sprintf("failed to list source versions for garbage collection: %s", err),
			}); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to patch ManagedCloudProfile status: %w (original error: %w)", patchErr, err)
			}
			return ctrl.Result{}, fmt.Errorf("failed to list source versions for garbage collection: %w", err)
		}

		referencedVersions := r.getReferencedVersions(ctx, mcp.Name, updates.ImageName)

		cutoff := time.Now().Add(-updates.GarbageCollection.MaxAge.Duration)
		versionsToDelete := make([]string, 0)
		for _, v := range versions {
			if v.CreatedAt.IsZero() {
				continue
			}
			if _, isReferenced := referencedVersions[v.Version]; isReferenced {
				continue
			}
			if v.CreatedAt.Before(cutoff) {
				versionsToDelete = append(versionsToDelete, v.Version)
			}
		}

		if len(versionsToDelete) > 0 {
			if err := r.deleteVersion(ctx, mcp.Name, updates.ImageName, versionsToDelete); err != nil {
				if apierrors.IsInvalid(err) {
					log.V(1).Info("garbage collection validation failed, skipping", "image", updates.ImageName)
					continue
				}
				if patchErr := r.patchStatusAndCondition(ctx, &mcp, v1alpha1.FailedReconcileStatus, metav1.Condition{
					Type:               CloudProfileAppliedConditionType,
					Status:             metav1.ConditionFalse,
					ObservedGeneration: mcp.Generation,
					Reason:             "GarbageCollectionFailed",
					Message:            fmt.Sprintf("failed to list source versions for garbage collection: %s", err),
				}); patchErr != nil {
					return ctrl.Result{}, fmt.Errorf("failed to patch ManagedCloudProfile status: %w (original error: %w)", patchErr, err)
				}
				return ctrl.Result{}, fmt.Errorf("failed to delete image versions: %w", err)
			}
			for _, v := range versionsToDelete {
				log.Info("deleted image version from CloudProfile", "image", updates.ImageName, "version", v)
			}
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *Reconciler) deleteVersion(ctx context.Context, cloudProfileName, imageName string, versions []string) error {
	var cp gardenerv1beta1.CloudProfile
	if err := r.Get(ctx, types.NamespacedName{Name: cloudProfileName}, &cp); err != nil {
		return err
	}

	versionsSet := make(map[string]bool)
	for _, v := range versions {
		versionsSet[v] = true
	}

	for i := range cp.Spec.MachineImages {
		if cp.Spec.MachineImages[i].Name != imageName {
			continue
		}
		newVersions := make([]gardenerv1beta1.MachineImageVersion, 0, len(cp.Spec.MachineImages[i].Versions))
		for _, mv := range cp.Spec.MachineImages[i].Versions {
			if !versionsSet[mv.Version] {
				newVersions = append(newVersions, mv)
			}
		}
		cp.Spec.MachineImages[i].Versions = newVersions
	}
	if cp.Spec.ProviderConfig != nil {
		var cfg providercfg.CloudProfileConfig
		if err := json.Unmarshal(cp.Spec.ProviderConfig.Raw, &cfg); err == nil {
			for i := range cfg.MachineImages {
				if cfg.MachineImages[i].Name != imageName {
					continue
				}
				filtered := make([]providercfg.MachineImageVersion, 0, len(cfg.MachineImages[i].Versions))
				for _, mv := range cfg.MachineImages[i].Versions {
					found := false
					for _, version := range versions {
						if strings.HasSuffix(mv.Image, ":"+version) {
							found = true
							break
						}
					}
					if !found {
						filtered = append(filtered, mv)
					}
				}
				cfg.MachineImages[i].Versions = filtered
			}
			raw, err := json.Marshal(cfg)
			if err == nil {
				cp.Spec.ProviderConfig.Raw = raw
			}
		}
	}

	return r.Update(ctx, &cp)
}

func (r *Reconciler) getReferencedVersions(ctx context.Context, cloudProfileName, imageName string) map[string]bool {
	referenced := make(map[string]bool)

	var cp gardenerv1beta1.CloudProfile
	if err := r.Get(ctx, types.NamespacedName{Name: cloudProfileName}, &cp); err == nil {
		if cp.Spec.ProviderConfig != nil {
			var cfg providercfg.CloudProfileConfig
			if err := json.Unmarshal(cp.Spec.ProviderConfig.Raw, &cfg); err == nil {
				for _, img := range cfg.MachineImages {
					if img.Name != imageName {
						continue
					}
					for _, v := range img.Versions {
						if idx := strings.LastIndex(v.Image, ":"); idx != -1 {
							version := v.Image[idx+1:]
							referenced[version] = true
						}
					}
				}
			}
		}
	}

	shootList := &gardenerv1beta1.ShootList{}
	if err := r.List(ctx, shootList, client.InNamespace(metav1.NamespaceAll)); err == nil {
		for _, shoot := range shootList.Items {
			if shoot.Spec.CloudProfile == nil || shoot.Spec.CloudProfile.Name != cloudProfileName {
				continue
			}

			for _, worker := range shoot.Spec.Provider.Workers {
				if worker.Machine.Image == nil || worker.Machine.Image.Name != imageName {
					continue
				}
				if worker.Machine.Image.Version != nil {
					referenced[*worker.Machine.Image.Version] = true
				}
			}
		}
	}

	return referenced
}

func (r *Reconciler) updateMachineImages(ctx context.Context, log logr.Logger, update v1alpha1.MachineImageUpdate, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	var source cloudprofilesync.Source
	switch {
	case update.Source.OCI != nil:
		password, err := r.getCredential(ctx, update.Source.OCI.Password)
		if err != nil {
			return err
		}
		src, err := OCIFactory(cloudprofilesync.OCIParams{
			Registry:   update.Source.OCI.Registry,
			Repository: update.Source.OCI.Repository,
			Username:   update.Source.OCI.Username,
			Password:   string(password),
			Parallel:   1,
		}, update.Source.OCI.Insecure)
		if err != nil {
			return fmt.Errorf("failed to initialize oci source: %w", err)
		}
		source = src
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

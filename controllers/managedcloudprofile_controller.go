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

// OCISourceFactory defines an interface for creating OCI sources.
type OCISourceFactory interface {
	Create(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error)
}

// DefaultOCISourceFactory is the default implementation of OCISourceFactory.
type DefaultOCISourceFactory struct{}

func (f *DefaultOCISourceFactory) Create(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error) {
	return cloudprofilesync.NewOCI(params, insecure)
}

type Reconciler struct {
	client.Client
	OCISourceFactory OCISourceFactory
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	var mcp v1alpha1.ManagedCloudProfile
	if err := r.Get(ctx, req.NamespacedName, &mcp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("starting reconciliation", "ManagedCloudProfile", mcp.Name, "generation", mcp.Generation)

	if err := r.reconcileCloudProfile(ctx, log, &mcp); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconciled ManagedCloudProfile")
	if err := r.reconcileGarbageCollection(ctx, log, &mcp); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *Reconciler) reconcileCloudProfile(ctx context.Context, log logr.Logger, mcp *v1alpha1.ManagedCloudProfile) error {
	var cloudProfile gardenerv1beta1.CloudProfile
	cloudProfile.Name = mcp.Name
	log.V(1).Info("starting CloudProfile reconciliation", "name", cloudProfile.Name, "generation", mcp.Generation)

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, &cloudProfile, func() error {
		if err := controllerutil.SetControllerReference(mcp, &cloudProfile, r.Scheme()); err != nil {
			log.Error(err, "failed to set controller reference")
			return err
		}
		log.V(1).Info("controller reference set")
		cloudProfile.Spec = CloudProfileSpecToGardener(&mcp.Spec.CloudProfile)
		totalVersionsBefore := 0
		for _, img := range cloudProfile.Spec.MachineImages {
			totalVersionsBefore += len(img.Versions)
			log.Info("before update - machine image summary", "image", img.Name, "versions_count", len(img.Versions))
		}
		log.Info("total machine image versions before update", "total_versions_before", totalVersionsBefore)
		errs := make([]error, 0)
		for _, updates := range mcp.Spec.MachineImageUpdates {
			log.V(1).Info("updating machine images from source", "imageName", updates.ImageName)
			if updateErr := r.updateMachineImages(ctx, log, updates, &cloudProfile.Spec); updateErr != nil {
				log.Error(updateErr, "failed to update machine images", "imageName", updates.ImageName)
				errs = append(errs, updateErr)
			}
		}

		totalVersionsAfter := 0
		for _, img := range cloudProfile.Spec.MachineImages {
			totalVersionsAfter += len(img.Versions)
			log.Info("after update - machine image summary", "image", img.Name, "versions_count", len(img.Versions))
		}
		log.Info("total machine image versions after update", "total_versions_after", totalVersionsAfter)

		gardenerv1beta1.SetObjectDefaults_CloudProfile(&cloudProfile)
		log.V(1).Info("set CloudProfile defaults")
		return errors.Join(errs...)
	})
	if err != nil {
		statusErr := r.patchStatusAndCondition(ctx, mcp, v1alpha1.FailedReconcileStatus, metav1.Condition{
			Type:               CloudProfileAppliedConditionType,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: mcp.Generation,
			Reason:             "ApplyFailed",
			Message:            fmt.Sprintf("Failed to apply CloudProfile: %s", err),
		})
		if statusErr != nil {
			log.Error(statusErr, "failed to patch ManagedCloudProfile status")
			return fmt.Errorf("failed to patch ManagedCloudProfile status: %w", statusErr)
		}
		if apierrors.IsInvalid(err) {
			log.Error(err, "CloudProfile invalid, skipping apply")
			return nil
		}
		log.Error(err, "failed to create or patch CloudProfile")
		return fmt.Errorf("failed to create or patch CloudProfile: %w", err)
	}
	log.Info("applied cloud profile", "op", op)
	if op != controllerutil.OperationResultNone {
		statusErr := r.patchStatusAndCondition(ctx, mcp, v1alpha1.SucceededReconcileStatus, metav1.Condition{
			Type:               CloudProfileAppliedConditionType,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: mcp.Generation,
			Reason:             "Applied",
			Message:            "Generated CloudProfile applied successfully",
		})
		if statusErr != nil {
			log.Error(statusErr, "failed to patch ManagedCloudProfile status after apply")
			return fmt.Errorf("failed to patch ManagedCloudProfile status: %w", statusErr)
		}
		log.V(1).Info("ManagedCloudProfile status updated to SucceededReconcileStatus")
	}
	log.V(1).Info("finished CloudProfile reconciliation", "name", cloudProfile.Name)
	return nil
}

func (r *Reconciler) reconcileGarbageCollection(ctx context.Context, log logr.Logger, mcp *v1alpha1.ManagedCloudProfile) error {
	log.V(1).Info("starting garbage collection", "ManagedCloudProfile", mcp.Name)
	if mcp.Spec.GarbageCollection == nil || !mcp.Spec.GarbageCollection.Enabled {
		log.V(1).Info("garbage collection disabled or not configured")
		return nil
	}
	if mcp.Spec.GarbageCollection.MaxAge.Duration < 0 {
		return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("invalid garbage collection maxAge: %s", mcp.Spec.GarbageCollection.MaxAge.String()))
	}

	cutoff := time.Now().Add(-mcp.Spec.GarbageCollection.MaxAge.Duration)
	log.V(1).Info("garbage collection cutoff time calculated", "cutoff", cutoff)

	for _, updates := range mcp.Spec.MachineImageUpdates {
		if updates.Source.OCI == nil {
			log.V(1).Info("skipping update with no OCI source", "image", updates.ImageName)
			continue
		}

		log.V(1).Info("fetching OCI credentials", "image", updates.ImageName)
		password, err := r.getCredential(ctx, updates.Source.OCI.Password)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("failed to get credential for garbage collection: %w", err))
		}

		src, err := r.OCISourceFactory.Create(cloudprofilesync.OCIParams{
			Registry:   updates.Source.OCI.Registry,
			Repository: updates.Source.OCI.Repository,
			Username:   updates.Source.OCI.Username,
			Password:   string(password),
			Parallel:   1,
		}, updates.Source.OCI.Insecure)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("failed to initialize OCI source for garbage collection: %w", err))
		}

		log.V(1).Info("retrieving source versions", "image", updates.ImageName)
		versions, err := src.GetVersions(ctx)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("failed to list source versions for garbage collection: %w", err))
		}
		log.V(1).Info("retrieved source versions", "count", len(versions), "image", updates.ImageName)

		referencedVersions, err := r.getReferencedVersions(ctx, mcp.Name, updates.ImageName, log)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("failed to determine referenced versions for garbage collection: %w", err))
		}
		log.V(1).Info("referenced versions retrieved", "count", len(referencedVersions), "image", updates.ImageName)

		versionsToDelete := make(map[string]struct{})
		for _, v := range versions {
			if v.CreatedAt.IsZero() {
				log.V(1).Info("skipping version with zero CreatedAt", "version", v.Version)
				continue
			}
			if _, isReferenced := referencedVersions[v.Version]; isReferenced {
				log.V(2).Info("skipping referenced version", "version", v.Version)
				continue
			}
			if v.CreatedAt.Before(cutoff) {
				versionsToDelete[v.Version] = struct{}{}
				log.V(1).Info("marking version for deletion", "version", v.Version)
			}
		}

		if len(versionsToDelete) > 0 {
			log.V(1).Info("deleting versions from CloudProfile", "image", updates.ImageName, "count", len(versionsToDelete))
			if err := r.deleteVersions(ctx, mcp.Name, updates.ImageName, versionsToDelete); err != nil {
				if apierrors.IsInvalid(err) {
					log.V(1).Info("garbage collection validation failed, skipping", "image", updates.ImageName)
					continue
				}
				return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("failed to delete image versions: %w", err))
			}
			for v := range versionsToDelete {
				log.Info("deleted image version from CloudProfile", "image", updates.ImageName, "version", v)
			}
		} else {
			log.V(1).Info("no versions to delete for image", "image", updates.ImageName)
		}
	}

	log.V(1).Info("completed garbage collection", "ManagedCloudProfile", mcp.Name)
	return nil
}

func (r *Reconciler) deleteVersions(ctx context.Context, cloudProfileName, imageName string, versionsToDelete map[string]struct{}) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("starting deleteVersions", "cloudProfile", cloudProfileName, "image", imageName, "versionsToDelete", versionsToDelete)
	var cp gardenerv1beta1.CloudProfile
	if err := r.Get(ctx, types.NamespacedName{Name: cloudProfileName}, &cp); err != nil {
		log.Error(err, "failed to get CloudProfile")
		return err
	}

	for i := range cp.Spec.MachineImages {
		if cp.Spec.MachineImages[i].Name != imageName {
			continue
		}
		originalCount := len(cp.Spec.MachineImages[i].Versions)
		cp.Spec.MachineImages[i].Versions = slices.DeleteFunc(cp.Spec.MachineImages[i].Versions, func(mv gardenerv1beta1.MachineImageVersion) bool {
			_, exists := versionsToDelete[mv.Version]
			if exists {
				log.V(1).Info("removing version from CloudProfile MachineImages", "version", mv.Version)
			}
			return exists
		})
		log.V(1).Info("updated CloudProfile MachineImages versions", "original", originalCount, "remaining", len(cp.Spec.MachineImages[i].Versions))
	}
	if cp.Spec.ProviderConfig != nil {
		var cfg providercfg.CloudProfileConfig
		if err := json.Unmarshal(cp.Spec.ProviderConfig.Raw, &cfg); err != nil {
			log.Error(err, "failed to unmarshal ProviderConfig")
			return fmt.Errorf("failed to unmarshal ProviderConfig: %w", err)
		}
		for i := range cfg.MachineImages {
			if cfg.MachineImages[i].Name != imageName {
				continue
			}
			originalCount := len(cfg.MachineImages[i].Versions)
			cfg.MachineImages[i].Versions = slices.DeleteFunc(cfg.MachineImages[i].Versions, func(mv providercfg.MachineImageVersion) bool {
				idx := strings.LastIndex(mv.Image, ":")
				if idx == -1 {
					return false
				}
				version := mv.Image[idx+1:]
				_, exists := versionsToDelete[version]
				if exists {
					log.V(1).Info("removing version from ProviderConfig", "version", version, "imageRef", mv.Image)
				}
				return exists
			})
			log.V(1).Info("updated ProviderConfig MachineImages versions", "original", originalCount, "remaining", len(cfg.MachineImages[i].Versions))
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			log.Error(err, "failed to marshal ProviderConfig after deletion")
			return fmt.Errorf("failed to marshal ProviderConfig: %w", err)
		}
		cp.Spec.ProviderConfig.Raw = raw
	}
	if err := r.Update(ctx, &cp); err != nil {
		log.Error(err, "failed to update CloudProfile after deleting versions")
		return err
	}
	log.V(1).Info("finished deleteVersions successfully")
	return nil
}

func (r *Reconciler) getReferencedVersions(ctx context.Context, cloudProfileName, imageName string, log logr.Logger) (map[string]struct{}, error) {
	referenced := make(map[string]struct{})
	log.V(1).Info("retrieving referenced versions", "cloudProfile", cloudProfileName, "image", imageName)

	var cp gardenerv1beta1.CloudProfile
	if err := r.Get(ctx, types.NamespacedName{Name: cloudProfileName}, &cp); err != nil {
		log.Error(err, "failed to get CloudProfile")
		return nil, fmt.Errorf("failed to get CloudProfile: %w", err)
	}

	if cp.Spec.ProviderConfig != nil {
		var cfg providercfg.CloudProfileConfig
		if err := json.Unmarshal(cp.Spec.ProviderConfig.Raw, &cfg); err != nil {
			log.Error(err, "failed to unmarshal ProviderConfig")
			return nil, fmt.Errorf("failed to unmarshal ProviderConfig: %w", err)
		}
		for _, img := range cfg.MachineImages {
			if img.Name != imageName {
				continue
			}
			for _, v := range img.Versions {
				if idx := strings.LastIndex(v.Image, ":"); idx != -1 {
					version := v.Image[idx+1:]
					referenced[version] = struct{}{}
					log.V(2).Info("found referenced version in ProviderConfig", "version", version)
				}
			}
		}
	}

	shootList := &gardenerv1beta1.ShootList{}
	if err := r.List(ctx, shootList, client.InNamespace(metav1.NamespaceAll)); err != nil {
		log.Error(err, "failed to list Shoots")
		return nil, fmt.Errorf("failed to list Shoots: %w", err)
	}
	for _, shoot := range shootList.Items {
		if shoot.Spec.CloudProfile == nil || shoot.Spec.CloudProfile.Name != cloudProfileName {
			continue
		}

		for _, worker := range shoot.Spec.Provider.Workers {
			if worker.Machine.Image == nil || worker.Machine.Image.Name != imageName {
				continue
			}
			if worker.Machine.Image.Version != nil {
				referenced[*worker.Machine.Image.Version] = struct{}{}
				log.V(2).Info("found referenced version in Shoot", "shoot", shoot.Name, "worker", worker.Name, "version", *worker.Machine.Image.Version)
			}
		}
	}

	log.V(1).Info("completed retrieval of referenced versions", "count", len(referenced))
	return referenced, nil
}

func (r *Reconciler) updateMachineImages(ctx context.Context, log logr.Logger, update v1alpha1.MachineImageUpdate, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	log.Info("updating machine images", "imageName", update.ImageName)
	var source cloudprofilesync.Source
	switch {
	case update.Source.OCI != nil:
		log.V(1).Info("using OCI source", "registry", update.Source.OCI.Registry, "repository", update.Source.OCI.Repository)
		password, err := r.getCredential(ctx, update.Source.OCI.Password)
		if err != nil {
			log.Error(err, "failed to get credentials for OCI source", "imageName", update.ImageName)
			return err
		}
		src, err := r.OCISourceFactory.Create(cloudprofilesync.OCIParams{
			Registry:   update.Source.OCI.Registry,
			Repository: update.Source.OCI.Repository,
			Username:   update.Source.OCI.Username,
			Password:   string(password),
			Parallel:   1,
		}, update.Source.OCI.Insecure)
		if err != nil {
			log.Error(err, "failed to initialize OCI source", "imageName", update.ImageName)
			return fmt.Errorf("failed to initialize OCI source: %w", err)
		}
		source = src

	default:
		log.Error(nil, "no machine images source configured", "imageName", update.ImageName)
		return errors.New("no machine images source configured")
	}

	var provider cloudprofilesync.Provider
	if update.Provider.IroncoreMetal != nil {
		log.V(1).Info("using provider IroncoreMetal", "registry", update.Provider.IroncoreMetal.Registry, "repository", update.Provider.IroncoreMetal.Repository)
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
	log.V(1).Info("calling ImageUpdater.Update", "imageName", update.ImageName)
	if err := imageUpdater.Update(ctx, cpSpec); err != nil {
		log.Error(err, "ImageUpdater.Update failed", "imageName", update.ImageName)
		return fmt.Errorf("updating machine images failed: %w", err)
	}
	log.Info("successfully updated machine images", "imageName", update.ImageName)
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

func (r *Reconciler) failWithStatusUpdate(ctx context.Context, mcp *v1alpha1.ManagedCloudProfile, returnErr error) error {
	if patchErr := r.patchStatusAndCondition(ctx, mcp, v1alpha1.FailedReconcileStatus, metav1.Condition{
		Type:               CloudProfileAppliedConditionType,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: mcp.Generation,
		Reason:             "GarbageCollectionFailed",
		Message:            returnErr.Error(),
	}); patchErr != nil {
		return fmt.Errorf("failed to patch ManagedCloudProfile status: %w (original error: %w)", patchErr, returnErr)
	}
	return returnErr
}

// SetupWithManager attaches the controller to the given manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.OCISourceFactory == nil {
		r.OCISourceFactory = &DefaultOCISourceFactory{}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ManagedCloudProfile{}).
		Owns(&gardenerv1beta1.CloudProfile{}).
		Complete(r)
}

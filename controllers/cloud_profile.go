// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"errors"
	"fmt"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/k8ssync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

func (r *Reconciler) reconcileCloudProfile(ctx context.Context, log logr.Logger, mcp *v1alpha1.ManagedCloudProfile) error {
	var cloudProfile gardenerv1beta1.CloudProfile
	cloudProfile.Name = mcp.Name

	base := &mcp.Spec.CloudProfile

	var imgs []gardenerv1beta1.MachineImage
	var providerConfig *runtime.RawExtension
	if !mcp.Spec.MachineImagesPaused {
		imgs = base.MachineImages
		providerConfig = base.ProviderConfig
		for _, update := range mcp.Spec.MachineImageUpdates {
			log.V(1).Info("updating machine images", "cloudProfile", cloudProfile.Name)
			nextImgs, nextPC, err := r.prepareMachineImageUpdate(ctx, log, mcp, imgs, providerConfig, update)
			if err != nil {
				if statusErr := r.markReconcileFailed(ctx, mcp, err); statusErr != nil {
					return statusErr
				}
				return err
			}
			imgs, providerConfig = nextImgs, nextPC
		}
	}
	var versions []gardenerv1beta1.ExpirableVersion
	if mcp.Spec.KubernetesUpdate != nil {
		log.V(1).Info("updating kubernetes versions", "cloudProfile", cloudProfile.Name)
		v, err := r.prepareKubernetesUpdate(ctx, *mcp.Spec.KubernetesUpdate)
		if err != nil {
			if statusErr := r.markReconcileFailed(ctx, mcp, err); statusErr != nil {
				return statusErr
			}
			return err
		}
		versions = v
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, &cloudProfile, func() error {
		if err := controllerutil.SetControllerReference(mcp, &cloudProfile, r.Scheme()); err != nil {
			return err
		}
		copyStaticSpecFields(&cloudProfile.Spec, base)

		if mcp.Spec.MachineImagesPaused {
			if len(cloudProfile.Spec.MachineImages) == 0 {
				cloudProfile.Spec.MachineImages = deepCopyMachineImages(base.MachineImages)
			}
			if cloudProfile.Spec.ProviderConfig == nil {
				cloudProfile.Spec.ProviderConfig = base.ProviderConfig.DeepCopy()
			}
		} else {
			outImages := deepCopyMachineImages(imgs)
			carryExpirationDates(cloudProfile.Spec.MachineImages, outImages)
			cloudProfile.Spec.MachineImages = outImages
			cloudProfile.Spec.ProviderConfig = providerConfig
		}
		if mcp.Spec.KubernetesUpdate != nil {
			cloudProfile.Spec.Kubernetes.Versions = versions
		}
		gardenerv1beta1.SetObjectDefaults_CloudProfile(&cloudProfile)
		return nil
	})
	log.V(1).Info("CloudProfile patch operation", "operation", op)
	if err != nil {
		if statusErr := r.markReconcileFailed(ctx, mcp, err); statusErr != nil {
			return statusErr
		}
		if apierrors.IsInvalid(err) {
			log.Error(err, "CloudProfile is invalid, skipping retry")
			return nil
		}
		return fmt.Errorf("failed to create or patch CloudProfile: %w", err)
	}
	statusErr := r.patchStatusAndCondition(ctx, mcp, v1alpha1.SucceededReconcileStatus, metav1.Condition{
		Type:               CloudProfileAppliedConditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: mcp.Generation,
		Reason:             "Applied",
		Message:            "Generated CloudProfile applied successfully",
	})
	if statusErr != nil {
		return fmt.Errorf("failed to patch ManagedCloudProfile status: %w", statusErr)
	}
	return nil
}

func copyStaticSpecFields(dst *gardenerv1beta1.CloudProfileSpec, src *v1alpha1.CloudProfileSpec) {
	cpu := src.DeepCopy()
	dst.CABundle = cpu.CABundle
	dst.Kubernetes = cpu.Kubernetes
	dst.MachineTypes = cpu.MachineTypes
	dst.Regions = cpu.Regions
	dst.SeedSelector = cpu.SeedSelector
	dst.Type = cpu.Type
	dst.VolumeTypes = cpu.VolumeTypes
	dst.Bastion = cpu.Bastion
	dst.Limits = cpu.Limits
	dst.MachineCapabilities = cpu.MachineCapabilities
}

// deepCopyMachineImages returns a deep copy of the given machine images so
// in-place edits (e.g. carrying expiration dates) never mutate the shared
// pre-fetched slice across CreateOrPatch retries.
func deepCopyMachineImages(images []gardenerv1beta1.MachineImage) []gardenerv1beta1.MachineImage {
	if images == nil {
		return nil
	}
	out := make([]gardenerv1beta1.MachineImage, len(images))
	for i := range images {
		images[i].DeepCopyInto(&out[i])
	}
	return out
}

func (r *Reconciler) markReconcileFailed(ctx context.Context, mcp *v1alpha1.ManagedCloudProfile, err error) error {
	statusErr := r.patchStatusAndCondition(ctx, mcp, v1alpha1.FailedReconcileStatus, metav1.Condition{
		Type:               CloudProfileAppliedConditionType,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: mcp.Generation,
		Reason:             "ApplyFailed",
		Message:            truncateConditionMessage(fmt.Sprintf("Failed to apply CloudProfile: %s", err)),
	})
	if statusErr != nil {
		return fmt.Errorf("failed to patch ManagedCloudProfile status: %w", statusErr)
	}
	return nil
}

func (r *Reconciler) prepareMachineImageUpdate(
	ctx context.Context,
	log logr.Logger,
	mcp *v1alpha1.ManagedCloudProfile,
	baseImages []gardenerv1beta1.MachineImage,
	baseProviderConfig *runtime.RawExtension,
	update v1alpha1.MachineImageUpdate,
) ([]gardenerv1beta1.MachineImage, *runtime.RawExtension, error) {
	source, err := r.selectSource(ctx, log, update.Source)
	if err != nil {
		return nil, nil, err
	}
	provider, err := selectProvider(update, r.EnableCapabilities)
	if err != nil {
		return nil, nil, err
	}

	filter, err := r.buildGCFilter(ctx, mcp, update)
	if err != nil {
		log.Error(err, "skipping garbage collection for machine image", "image", update.ImageName)
	}

	updater := ossync.ImageUpdater{
		Log:                log,
		Source:             source,
		Provider:           provider,
		ImageName:          update.ImageName,
		EnableCapabilities: r.EnableCapabilities,
		Filter:             filter,
	}
	sourceImages, err := updater.Fetch(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching machine images failed: %w", err)
	}
	images, providerConfig, err := updater.Apply(mcp.Spec.CloudProfile.MachineCapabilities, baseImages, baseProviderConfig, sourceImages)
	if err != nil {
		return nil, nil, fmt.Errorf("applying machine images failed: %w", err)
	}
	versionCount := 0
	for _, img := range images {
		if img.Name == update.ImageName {
			versionCount = len(img.Versions)
		}
	}
	log.Info("prepared machine image update", "image", update.ImageName, "sourceImages", len(sourceImages), "versions", versionCount)
	return images, providerConfig, nil
}

func (r *Reconciler) prepareKubernetesUpdate(ctx context.Context, cfg v1alpha1.KubernetesVersionUpdateConfig) ([]gardenerv1beta1.ExpirableVersion, error) {
	var source k8ssync.KubernetesVersionSource
	var err error
	switch {
	case cfg.LandscapeSetup != nil:
		source, err = r.landscapeSetupSource(ctx, *cfg.LandscapeSetup)
		if err != nil {
			return nil, fmt.Errorf("getting landscape setup source: %w", err)
		}
	default:
		return nil, errors.New("no kubernetes version source configured")
	}

	updater := k8ssync.NewKubernetesVersionUpdater(source, cfg.ExpirationThreshold.Duration)
	fetched, err := updater.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching kubernetes versions failed: %w", err)
	}
	versions, err := updater.Apply(fetched)
	if err != nil {
		return nil, fmt.Errorf("applying kubernetes versions failed: %w", err)
	}
	return versions, nil
}

func carryExpirationDates(existing, next []gardenerv1beta1.MachineImage) {
	stored := make(map[string]*metav1.Time)
	for _, img := range existing {
		for _, v := range img.Versions {
			if v.ExpirationDate != nil { //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
				stored[expirationDateKey(img.Name, v.Version)] = v.ExpirationDate //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
			}
		}
	}
	for i := range next {
		for j := range next[i].Versions {
			v := &next[i].Versions[j]
			isDeprecated := v.Classification != nil && *v.Classification == gardenerv1beta1.ClassificationDeprecated //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
			if !isDeprecated {
				continue
			}
			if exp, ok := stored[expirationDateKey(next[i].Name, v.Version)]; ok {
				v.ExpirationDate = exp //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
			}
		}
	}
}

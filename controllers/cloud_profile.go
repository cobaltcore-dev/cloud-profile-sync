// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"errors"
	"fmt"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

// DefaultOCISourceFactory is the default implementation of OCISourceFactory.
type DefaultOCISourceFactory struct{}

func (f *DefaultOCISourceFactory) Create(params cloudprofilesync.OCIParams, insecure bool, log logr.Logger) (cloudprofilesync.Source, error) {
	return cloudprofilesync.NewOCI(params, insecure, log)
}

func (r *Reconciler) reconcileCloudProfile(ctx context.Context, log logr.Logger, mcp *v1alpha1.ManagedCloudProfile) error {
	var cloudProfile gardenerv1beta1.CloudProfile
	cloudProfile.Name = mcp.Name

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, &cloudProfile, func() error {
		if err := controllerutil.SetControllerReference(mcp, &cloudProfile, r.Scheme()); err != nil {
			return err
		}
		cloudProfile.Spec = CloudProfileSpecToGardener(&mcp.Spec.CloudProfile)
		errs := make([]error, 0)
		for _, updates := range mcp.Spec.MachineImageUpdates {
			if updateErr := r.updateMachineImages(ctx, log, updates, &cloudProfile.Spec); updateErr != nil {
				errs = append(errs, updateErr)
			}
		}
		if mcp.Spec.KubernetesVersionUpdateConfig != nil {
			if updateErr := r.updateKubernetesVersions(ctx, *mcp.Spec.KubernetesVersionUpdateConfig, &cloudProfile.Spec); updateErr != nil {
				errs = append(errs, updateErr)
			}
		}
		gardenerv1beta1.SetObjectDefaults_CloudProfile(&cloudProfile)
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
			return fmt.Errorf("failed to patch ManagedCloudProfile status: %w", statusErr)
		}
		if apierrors.IsInvalid(err) {
			return nil
		}
		return fmt.Errorf("failed to create or patch CloudProfile: %w", err)
	}
	if op != controllerutil.OperationResultNone {
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
	}
	return nil
}

func (r *Reconciler) updateMachineImages(ctx context.Context, log logr.Logger, update v1alpha1.MachineImageUpdate, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	var source cloudprofilesync.Source
	switch {
	case update.Source.OCI != nil:
		password, err := r.getCredential(ctx, update.Source.OCI.Password)
		if err != nil {
			return err
		}
		src, err := r.OCISourceFactory.Create(cloudprofilesync.OCIParams{
			Registry:   update.Source.OCI.Registry,
			Repository: update.Source.OCI.Repository,
			Username:   update.Source.OCI.Username,
			Password:   string(password),
			Parallel:   1,
		}, update.Source.OCI.Insecure, log)
		if err != nil {
			return fmt.Errorf("failed to initialize OCI source: %w", err)
		}
		source = src

	default:
		return errors.New("no machine images source configured")
	}

	var provider cloudprofilesync.Provider
	if update.Provider.IroncoreMetal != nil {
		provider = &cloudprofilesync.IroncoreProvider{
			Registry:           update.Provider.IroncoreMetal.Registry,
			Repository:         update.Provider.IroncoreMetal.Repository,
			ImageName:          update.ImageName,
			EnableCapabilities: r.EnableCapabilities,
		}
	}
	imageUpdater := cloudprofilesync.ImageUpdater{
		Log:                log,
		Source:             source,
		Provider:           provider,
		ImageName:          update.ImageName,
		EnableCapabilities: r.EnableCapabilities,
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

func (r *Reconciler) updateKubernetesVersions(ctx context.Context, update v1alpha1.KubernetesVersionUpdateConfig, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	var source cloudprofilesync.KubernetesImageProvider
	switch {
	case update.Source.Github != nil:
		pat, err := r.getCredential(ctx, update.Source.Github.PersonalAccessTokenSecret)
		if err != nil {
			return err
		}
		source = cloudprofilesync.NewGithubKubernetesSource(update.Source.Github.URL, string(pat), update.Source.Github.Provider)
	case update.Source.Keppel != nil:
		password, err := r.getCredential(ctx, update.Source.Keppel.Password)
		if err != nil {
			return err
		}
		src, err := cloudprofilesync.NewKeppelKubernetesSource(cloudprofilesync.KeppelParams{
			Registry:     update.Source.Keppel.Registry,
			Repository:   update.Source.Keppel.Repository,
			Username:     update.Source.Keppel.Username,
			Password:     string(password),
			ResourceName: update.Source.Keppel.ResourceName,
		}, update.Source.Keppel.Insecure)
		if err != nil {
			return fmt.Errorf("failed to initialize Keppel source: %w", err)
		}
		source = src
	default:
		return errors.New("no kubernetes version provider configured")
	}

	kubernetesUpdater := cloudprofilesync.NewKubernetesImageUpdater(source, update.ExpirationThreshold.Duration)
	if err := kubernetesUpdater.Update(ctx, cpSpec); err != nil {
		return fmt.Errorf("updating kubernetes versions failed: %w", err)
	}

	return nil
}

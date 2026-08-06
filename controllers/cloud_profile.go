// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/kubernetessync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/kubernetessync/source/landscape"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/provider/ironcore"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/source/oci"
)

// DefaultOCISourceFactory is the default implementation of OCISourceFactory.
type DefaultOCISourceFactory struct{}

func (f *DefaultOCISourceFactory) Create(params oci.Params, parallel int64, log logr.Logger) (ossync.Source, error) {
	return oci.NewOCI(params, parallel, log)
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
	var source ossync.Source
	switch {
	case update.Source.OCI != nil:
		password, err := r.getCredential(ctx, update.Source.OCI.Password)
		if err != nil {
			return err
		}
		src, err := r.OCISourceFactory.Create(oci.Params{
			Registry:   update.Source.OCI.Registry,
			Repository: update.Source.OCI.Repository,
			Username:   update.Source.OCI.Username,
			Password:   string(password),
			Insecure:   update.Source.OCI.Insecure,
		}, 1, log)
		if err != nil {
			return fmt.Errorf("failed to initialize OCI source: %w", err)
		}
		source = src

	default:
		return errors.New("no machine images source configured")
	}

	var provider ossync.Provider
	if update.Provider.IroncoreMetal != nil {
		provider = &ironcore.IroncoreProvider{
			Registry:           update.Provider.IroncoreMetal.Registry,
			Repository:         update.Provider.IroncoreMetal.Repository,
			ImageName:          update.ImageName,
			EnableCapabilities: r.EnableCapabilities,
		}
	}
	imageUpdater := ossync.ImageUpdater{
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

type KubernetesImageUpdater interface {
	Update(ctx context.Context, cpSpec *gardenerv1beta1.CloudProfileSpec) error
}

func (r *Reconciler) updateKubernetesVersions(ctx context.Context, update v1alpha1.KubernetesVersionUpdateConfig, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	var source kubernetessync.KubernetesVersionSource
	var err error
	switch {
	case update.LandscapeSetup != nil:
		source, err = r.landscapeSetupSource(ctx, *update.LandscapeSetup)
		if err != nil {
			return fmt.Errorf("getting landscape setup source: %w", err)
		}
	default:
		return errors.New("no kubernetes version source configured")
	}

	kubernetesUpdater := kubernetessync.NewKubernetesImageUpdater(source, update.ExpirationThreshold.Duration)

	if err := kubernetesUpdater.Update(ctx, cpSpec); err != nil {
		return fmt.Errorf("updating kubernetes versions failed: %w", err)
	}

	return nil
}

func (r *Reconciler) landscapeSetupSource(ctx context.Context, ls v1alpha1.LandscapeSetup) (kubernetessync.KubernetesVersionSource, error) {
	ociPassword, err := r.getCredential(ctx, ls.OCI.Password)
	if err != nil {
		return nil, fmt.Errorf("getting oci password: %w", err)
	}
	ociParams := oci.Params{
		Registry:   ls.OCI.Registry,
		Repository: ls.OCI.Repository,
		Username:   ls.OCI.Username,
		Password:   string(ociPassword),
		Insecure:   ls.OCI.Insecure,
	}

	gh := ls.Github
	var ghTransport http.RoundTripper
	switch {
	case gh.PersonalAccessTokenSecret != nil:
		pat, err := r.getCredential(ctx, *gh.PersonalAccessTokenSecret)
		if err != nil {
			return nil, fmt.Errorf("getting github PAT: %w", err)
		}
		ghTransport = landscape.GithubPATTransport(gh.RepositoryApiURL, string(pat))
	case gh.GithubApp != nil:
		privateKey, err := r.getCredential(ctx, gh.GithubApp.PrivateKeySecret)
		if err != nil {
			return nil, fmt.Errorf("getting github app private key: %w", err)
		}
		ghTransport, err = landscape.GithubAppTransport(gh.RepositoryApiURL, gh.GithubApp.AppID, gh.GithubApp.InstallationID, privateKey)
		if err != nil {
			return nil, fmt.Errorf("initializing github app transport: %w", err)
		}
	default:
		return nil, errors.New("github source requires personalAccessTokenSecret or githubApp")
	}

	ghParams := landscape.GithubParams{
		RepositoryApiURL: gh.RepositoryApiURL,
		Repository:       gh.Repository,
		FilePath:         gh.FilePath,
		Provider:         gh.Provider,
		Transport:        ghTransport,
	}

	landscapeSource, err := landscape.NewLandscapeKubernetesSource(ociParams, ghParams)
	if err != nil {
		return nil, fmt.Errorf("initializing landscape source: %w", err)
	}

	return landscapeSource, nil
}

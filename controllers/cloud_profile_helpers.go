// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/k8ssync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/k8ssync/source/landscape"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ocirepo"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/provider/ironcore"
	osprovider "github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/provider/openstack"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/source/glance"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/source/oci"
)

// DefaultOCISourceFactory is the default implementation of OCISourceFactory.
type DefaultOCISourceFactory struct{}

func (f *DefaultOCISourceFactory) Create(params ocirepo.Params, parallel int64, log logr.Logger, featureToCapabilityMap map[string]string, imageFilter *v1alpha1.ImageFilter) (ossync.Source, error) {
	return oci.NewOCI(params, parallel, log, featureToCapabilityMap, imageFilter)
}

// selectSource constructs the machine image Source configured on the update.
func (r *Reconciler) selectSource(ctx context.Context, log logr.Logger, src v1alpha1.MachineImageUpdateSource) (ossync.Source, error) {
	switch {
	case src.OCI != nil:
		return r.newOCISource(ctx, log, src.OCI)
	case src.Glance != nil:
		return r.newGlanceSource(ctx, log, src.Glance)
	default:
		return nil, errors.New("no machine images source configured")
	}
}

func (r *Reconciler) newOCISource(ctx context.Context, log logr.Logger, oci *v1alpha1.OCI) (ossync.Source, error) {
	password, err := r.getCredential(ctx, oci.Password)
	if err != nil {
		return nil, err
	}
	source, err := r.OCISourceFactory.Create(ocirepo.Params{
		Registry:   oci.Registry,
		Repository: oci.Repository,
		Username:   oci.Username,
		Password:   string(password),
		Insecure:   oci.Insecure,
	}, 1, log, oci.FeatureToCapabilityMap, oci.ImageFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize OCI source: %w", err)
	}
	return source, nil
}

func (r *Reconciler) newGlanceSource(ctx context.Context, log logr.Logger, g *v1alpha1.GlanceSource) (ossync.Source, error) {
	password, err := r.getCredential(ctx, g.PasswordSecret)
	if err != nil {
		return nil, err
	}
	source, err := glance.NewGlance(glance.GlanceParams{
		AuthURLFormat:     g.AuthURLFormat,
		Regions:           g.Regions,
		NamePrefix:        g.NamePrefix,
		KeepLatest:        g.KeepLatest,
		VersionOffset:     g.VersionOffset,
		ExcludedSuffixes:  g.ExcludedSuffixes,
		Parallel:          g.Parallel,
		ProjectName:       g.ProjectName,
		ProjectDomainName: g.ProjectDomainName,
		Username:          g.Username,
		UserDomainName:    g.UserDomainName,
		Password:          string(password),
	}, log)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Glance source: %w", err)
	}
	return source, nil
}

// selectProvider constructs the machine image Provider configured on the update.
func selectProvider(update v1alpha1.MachineImageUpdate, enableCapabilities bool) (ossync.Provider, error) {
	switch {
	case update.Provider.IroncoreMetal != nil:
		return &ironcore.IroncoreProvider{
			Registry:           update.Provider.IroncoreMetal.Registry,
			Repository:         update.Provider.IroncoreMetal.Repository,
			ImageName:          update.ImageName,
			EnableCapabilities: enableCapabilities,
		}, nil
	case update.Provider.OpenStack != nil:
		return &osprovider.OpenStackProvider{
			ImageName:          update.ImageName,
			EnableCapabilities: enableCapabilities,
		}, nil
	default:
		return nil, errors.New("no known provider configured")
	}
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

func (r *Reconciler) landscapeSetupSource(ctx context.Context, ls v1alpha1.LandscapeSetup) (k8ssync.KubernetesVersionSource, error) {
	ociPassword, err := r.getCredential(ctx, ls.OCI.Password)
	if err != nil {
		return nil, fmt.Errorf("getting oci password: %w", err)
	}
	ociParams := ocirepo.Params{
		Registry:   ls.OCI.Registry,
		Repository: ls.OCI.Repository,
		Username:   ls.OCI.Username,
		Password:   string(ociPassword),
		Insecure:   ls.OCI.Insecure,
	}
	landscapeSource, err := landscape.NewLandscapeKubernetesSource(ociParams, ls.Provider)
	if err != nil {
		return nil, fmt.Errorf("initializing landscape source: %w", err)
	}
	return landscapeSource, nil
}

const maxConditionMessageLen = 32768

func expirationDateKey(imageName, version string) string {
	return imageName + "/" + version
}

func truncateConditionMessage(msg string) string {
	if len(msg) <= maxConditionMessageLen {
		return msg
	}
	const suffix = "...[truncated]"
	return msg[:maxConditionMessageLen-len(suffix)] + suffix
}

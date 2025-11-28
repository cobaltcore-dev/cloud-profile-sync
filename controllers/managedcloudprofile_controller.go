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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
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

	_, err := controllerutil.CreateOrPatch(ctx, r.Client, &cloudProfile, func() error {
		err := controllerutil.SetControllerReference(&mcp, &cloudProfile, r.Scheme())
		if err != nil {
			return err
		}
		cloudProfile.Spec = *mcp.Spec.CloudProfile.DeepCopy()
		return r.updateMachineImages(ctx, log, mcp, &cloudProfile)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create or patch cloudprofile: %w", err)
	}
	log.Info("reconciled ManagedCloudProfile")
	return ctrl.Result{}, nil
}

func (r *Reconciler) updateMachineImages(ctx context.Context, log logr.Logger, mcp v1alpha1.ManagedCloudProfile, cloudProfile *gardenerv1beta1.CloudProfile) error {
	if mcp.Spec.MachineImages == nil {
		return nil
	}
	mi := *mcp.Spec.MachineImages
	var source cloudprofilesync.Source
	switch {
	case mi.Source.OCI != nil:
		password, err := r.getCredential(ctx, mi.Source.OCI.Password)
		if err != nil {
			return err
		}
		oci, err := cloudprofilesync.NewOCI(cloudprofilesync.OCIParams{
			Registry:   mi.Source.OCI.Registry,
			Repository: mi.Source.OCI.Repository,
			Username:   mi.Source.OCI.Username,
			Password:   string(password),
			Parallel:   1,
		}, mi.Source.OCI.Insecure)
		if err != nil {
			return fmt.Errorf("failed to initialize oci source: %w", err)
		}
		source = oci
	default:
		return errors.New("no machine images source configured")
	}

	var provider cloudprofilesync.Provider
	if mi.Provider.IroncoreMetal != nil {
		provider = &cloudprofilesync.IroncoreProvider{
			Registry:   mi.Provider.IroncoreMetal.Registry,
			Repository: mi.Provider.IroncoreMetal.Repository,
			ImageName:  mi.ImageName,
		}
	}
	updater := cloudprofilesync.ImageUpdater{
		Log:       log,
		Source:    source,
		Provider:  provider,
		ImageName: mi.ImageName,
	}
	if err := updater.Update(ctx, cloudProfile); err != nil {
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

// SetupWithManager attaches the controller to the given manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ManagedCloudProfile{}).
		Complete(r)
}

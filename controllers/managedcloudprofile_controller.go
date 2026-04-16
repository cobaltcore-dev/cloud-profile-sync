// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

type RegistryClient interface {
	GetTags(ctx context.Context, registry, repository string) (map[string]time.Time, error)
}

type KeppelClient struct{}

func (k *KeppelClient) GetTags(ctx context.Context, registry, repository string) (map[string]time.Time, error) {
	return fetchKeppelTags(ctx, registry, repository)
}

// DefaultOCISourceFactory is the default implementation of OCISourceFactory.
type DefaultOCISourceFactory struct{}

func (f *DefaultOCISourceFactory) Create(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error) {
	return cloudprofilesync.NewOCI(params, insecure)
}

type Reconciler struct {
	client.Client
	OCISourceFactory     OCISourceFactory
	RegistryProviderFunc func(registry string) (RegistryClient, error)
}

type KeppelTag struct {
	Name     string `json:"name"`
	PushedAt int64  `json:"pushed_at"`
}

type KeppelManifest struct {
	Digest   string      `json:"digest"`
	PushedAt int64       `json:"pushed_at"`
	Tags     []KeppelTag `json:"tags"`
}

type KeppelManifestsResponse struct {
	Manifests []KeppelManifest `json:"manifests"`
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

func (r *Reconciler) reconcileGarbageCollection(ctx context.Context, mcp *v1alpha1.ManagedCloudProfile) error {
	if mcp.Spec.GarbageCollection == nil || !mcp.Spec.GarbageCollection.Enabled {
		return nil
	}
	if mcp.Spec.GarbageCollection.MaxAge.Duration < 0 {
		return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("invalid garbage collection maxAge: %s", mcp.Spec.GarbageCollection.MaxAge.String()))
	}

	cutoff := time.Now().Add(-mcp.Spec.GarbageCollection.MaxAge.Duration)

	for _, updates := range mcp.Spec.MachineImageUpdates {
		if updates.Source.OCI == nil {
			continue
		}

		registryClient, err := r.RegistryProviderFunc(updates.Source.OCI.Registry)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp,
				fmt.Errorf("no registry provider found for registry %q: %w", updates.Source.OCI.Registry, err))
		}
		tags, err := registryClient.GetTags(
			ctx,
			updates.Source.OCI.Registry,
			updates.Source.OCI.Repository,
		)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp,
				fmt.Errorf("failed to fetch tags: %w", err))
		}

		referencedVersions, err := r.getReferencedVersions(ctx, mcp.Name, updates.ImageName)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("failed to determine referenced versions for garbage collection: %w", err))
		}

		versionsToDelete := make(map[string]struct{})
		for tag, pushedAt := range tags {
			if _, isReferenced := referencedVersions[tag]; isReferenced {
				continue
			}
			if pushedAt.Before(cutoff) {
				versionsToDelete[tag] = struct{}{}
			}
		}

		if len(versionsToDelete) > 0 {
			if err := r.deleteVersions(ctx, mcp.Name, updates.ImageName, versionsToDelete); err != nil {
				if apierrors.IsInvalid(err) {
					continue
				}
				return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("failed to delete image versions: %w", err))
			}
		}
	}

	return nil
}

func (r *Reconciler) deleteVersions(ctx context.Context, cloudProfileName, imageName string, versionsToDelete map[string]struct{}) error {
	var cp gardenerv1beta1.CloudProfile
	if err := r.Get(ctx, types.NamespacedName{Name: cloudProfileName}, &cp); err != nil {
		return err
	}

	for i := range cp.Spec.MachineImages {
		if cp.Spec.MachineImages[i].Name != imageName {
			continue
		}
		cp.Spec.MachineImages[i].Versions = slices.DeleteFunc(cp.Spec.MachineImages[i].Versions, func(mv gardenerv1beta1.MachineImageVersion) bool {
			_, exists := versionsToDelete[mv.Version]
			return exists
		})
	}
	if cp.Spec.ProviderConfig != nil {
		var cfg providercfg.CloudProfileConfig
		if err := json.Unmarshal(cp.Spec.ProviderConfig.Raw, &cfg); err != nil {
			return fmt.Errorf("failed to unmarshal ProviderConfig: %w", err)
		}
		for i := range cfg.MachineImages {
			if cfg.MachineImages[i].Name != imageName {
				continue
			}
			cfg.MachineImages[i].Versions = slices.DeleteFunc(cfg.MachineImages[i].Versions, func(mv providercfg.MachineImageVersion) bool {
				idx := strings.LastIndex(mv.Image, ":")
				if idx == -1 {
					return false
				}
				version := mv.Image[idx+1:]
				_, exists := versionsToDelete[version]
				return exists
			})
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("failed to marshal ProviderConfig: %w", err)
		}
		cp.Spec.ProviderConfig.Raw = raw
	}
	if err := r.Update(ctx, &cp); err != nil {
		return err
	}
	return nil
}

func (r *Reconciler) getReferencedVersions(ctx context.Context, cloudProfileName, imageName string) (map[string]struct{}, error) {
	referenced := make(map[string]struct{})

	shootList := &gardenerv1beta1.ShootList{}
	if err := r.List(ctx, shootList, client.InNamespace(metav1.NamespaceAll)); err != nil {
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
			}
		}
	}

	return referenced, nil
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
		}, update.Source.OCI.Insecure)
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

func fetchKeppelTags(ctx context.Context, registry, repository string) (map[string]time.Time, error) {
	baseURL := registryBaseURL(registry, false)

	keppelURL, err := keppelURL(baseURL, repository)
	if err != nil {
		return nil, fmt.Errorf("failed to build keppel URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, keppelURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create keppel request: %w", err)
	}

	tr := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: tr,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("keppel API returned status %d", resp.StatusCode)
		return nil, err
	}

	var result KeppelManifestsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	tagMap := make(map[string]time.Time)

	for _, m := range result.Manifests {
		for _, t := range m.Tags {
			if t.PushedAt == 0 {
				continue
			}
			tagMap[t.Name] = time.Unix(t.PushedAt, 0)
		}
	}

	return tagMap, nil
}

func keppelURL(baseURL, repository string) (string, error) {
	account, repo, err := splitKeppelRepository(repository)
	if err != nil {
		return "", err
	}

	keppelURL := fmt.Sprintf(
		"%s/keppel/v1/accounts/%s/repositories/%s/_manifests",
		baseURL,
		account,
		repo,
	)

	return keppelURL, nil
}

func registryBaseURL(registryHost string, insecure bool) string {
	scheme := "https"
	if insecure {
		scheme = "http"
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   registryHost,
	}

	base := u.String()

	return base
}

func splitKeppelRepository(repository string) (account, repo string, err error) {
	parts := strings.SplitN(repository, "/", 2)

	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		err := fmt.Errorf("invalid repository format %q, must be <account>/<repository-path>", repository)

		return "", "", err
	}

	account = parts[0]
	repo = parts[1]

	return account, repo, nil
}

func (r *Reconciler) getRegistryProvider(registry string) (registryClient RegistryClient, err error) {
	if registry == "" {
		return nil, errors.New("registry cannot be empty")
	}
	if strings.Contains(strings.ToLower(registry), "keppel") {
		return &KeppelClient{}, nil
	}

	return nil, errors.New("no registry provider found for registry")
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

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
		log.V(1).Info("converted ManagedCloudProfile spec to CloudProfileSpec", "machineImages", len(cloudProfile.Spec.MachineImages))
		errs := make([]error, 0)
		for _, updates := range mcp.Spec.MachineImageUpdates {
			log.V(1).Info("updating machine images from source", "imageName", updates.ImageName)
			if updateErr := r.updateMachineImages(ctx, log, updates, &cloudProfile.Spec); updateErr != nil {
				log.Error(updateErr, "failed to update machine images", "imageName", updates.ImageName)
				errs = append(errs, updateErr)
			}
		}
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

		log.V(1).Info("retrieving source registry", "registry", updates.Source.OCI.Registry)
		registryClient, err := r.RegistryProviderFunc(updates.Source.OCI.Registry)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp,
				fmt.Errorf("no registry provider found for registry %q: %w", updates.Source.OCI.Registry, err))
		}

		log.V(1).Info("retrieving source versions", "image", updates.ImageName)
		ctx = logr.NewContext(ctx, log)
		tags, err := registryClient.GetTags(
			ctx,
			updates.Source.OCI.Registry,
			updates.Source.OCI.Repository,
		)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp,
				fmt.Errorf("failed to fetch tags: %w", err))
		}
		log.V(1).Info("retrieved source versions", "count", len(tags))

		referencedVersions, err := r.getReferencedVersions(ctx, mcp.Name, updates.ImageName, log)
		if err != nil {
			return r.failWithStatusUpdate(ctx, mcp, fmt.Errorf("failed to determine referenced versions for garbage collection: %w", err))
		}
		log.V(1).Info("referenced versions retrieved", "count", len(referencedVersions), "image", updates.ImageName)

		versionsToDelete := make(map[string]struct{})
		for tag, pushedAt := range tags {
			if _, isReferenced := referencedVersions[tag]; isReferenced {
				log.V(2).Info("skipping referenced version", "version", tag)
				continue
			}
			if pushedAt.Before(cutoff) {
				versionsToDelete[tag] = struct{}{}
				log.V(1).Info("marking version for deletion", "version", tag, "pushedAt", pushedAt)
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
				log.V(1).Info("found referenced version in Shoot", "shoot", shoot.Name, "worker", worker.Name, "version", *worker.Machine.Image.Version)
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

func fetchKeppelTags(ctx context.Context, registry, repository string) (map[string]time.Time, error) {
	log, err := logr.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to extract logger from context: %w", err)
	}
	baseURL := registryBaseURL(log, registry, false)

	keppelURL, err := keppelURL(log, baseURL, repository)
	if err != nil {
		log.Error(err, "failed to build keppel URL",
			"registry", registry,
			"repository", repository,
		)
		return nil, err
	}

	log.V(1).Info("fetching keppel tags",
		"url", keppelURL,
		"registry", registry,
		"repository", repository,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, keppelURL, http.NoBody)
	if err != nil {
		log.Error(err, "failed to create keppel request")
		return nil, err
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
		log.Error(err, "keppel http request failed", "url", keppelURL)
		return nil, err
	}
	defer resp.Body.Close()

	log.V(1).Info("keppel response received", "status", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("keppel API returned status %d", resp.StatusCode)
		log.Error(err, "unexpected keppel status code")
		return nil, err
	}

	var result KeppelManifestsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Error(err, "failed to decode keppel response")
		return nil, err
	}

	log.V(1).Info("decoded keppel response", "manifests", len(result.Manifests))

	tagMap := make(map[string]time.Time)

	for i, m := range result.Manifests {
		log.V(2).Info("processing manifest",
			"index", i,
			"digest", m.Digest,
			"tags", len(m.Tags),
		)

		for _, t := range m.Tags {
			if t.PushedAt == 0 {
				log.V(1).Info("tag without pushed at", "name", t.Name)
				continue
			}

			log.V(2).Info("processing tag",
				"tag", t.Name,
				"pushedAt", t.PushedAt,
			)
			tagMap[t.Name] = time.Unix(t.PushedAt, 0)
		}
	}

	log.V(1).Info("finished fetching keppel tags", "count", len(tagMap))

	return tagMap, nil
}

func keppelURL(log logr.Logger, baseURL, repository string) (string, error) {
	account, repo, err := splitKeppelRepository(log, repository)
	if err != nil {
		log.Error(err, "failed to split keppel repository", "repository", repository)
		return "", err
	}

	keppelURL := fmt.Sprintf(
		"%s/keppel/v1/accounts/%s/repositories/%s/_manifests",
		baseURL,
		account,
		repo,
	)

	log.V(1).Info("constructed keppel url",
		"baseURL", baseURL,
		"account", account,
		"repo", repo,
		"url", keppelURL,
	)

	return keppelURL, nil
}

func registryBaseURL(log logr.Logger, registryHost string, insecure bool) string {
	scheme := "https"
	if insecure {
		scheme = "http"
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   registryHost,
	}

	base := u.String()

	log.V(2).Info("computed registry base url",
		"registryHost", registryHost,
		"insecure", insecure,
		"baseURL", base,
	)

	return base
}

func splitKeppelRepository(log logr.Logger, repository string) (account, repo string, err error) {
	parts := strings.SplitN(repository, "/", 2)

	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		err := fmt.Errorf("invalid repository format %q, must be <account>/<repository-path>", repository)

		log.Error(err, "invalid keppel repository format",
			"repository", repository,
		)

		return "", "", err
	}

	account = parts[0]
	repo = parts[1]

	log.V(2).Info("split keppel repository",
		"repository", repository,
		"account", account,
		"repo", repo,
	)

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

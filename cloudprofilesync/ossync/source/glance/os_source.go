package glance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"golang.org/x/sync/semaphore"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

const (
	defaultGlanceNamePrefix = "gardenlinux-openstack-gardener_prod-amd64-"
	glanceRequestTimeout    = 60 * time.Second
	DefaultGlanceKeepLatest = 3
	// defaultGlanceParallel is the region query concurrency used when GlanceParams.Parallel
	// is not set.
	defaultGlanceParallel = 8
	usiVariantMarker      = "_usi"
)

type Result[T any] struct {
	value T
	err   error
}

// GlanceParams configures discovery of public gardenlinux images from OpenStack Glance.
type GlanceParams struct {
	// AuthURLFormat is the Keystone endpoint format string with a single "%s" verb for
	// the region, e.g. "https://identity-3.%s.cloud.sap/v3".
	AuthURLFormat string

	Regions []string

	// NamePrefix selects gardenlinux images by exact prefix. Empty means the default.
	NamePrefix string

	// KeepLatest limits the result to the newest N versions.
	KeepLatest int

	// Parallel bounds how many regions are queried concurrently.
	Parallel int64

	// ProjectName / ProjectDomainName scope the token.
	ProjectName       string
	ProjectDomainName string

	// Username / UserDomainName / Password authenticate the user.
	Username       string
	UserDomainName string
	Password       string
}

// Glance discovers public gardenlinux images from OpenStack Glance across regions.
type Glance struct {
	log          logr.Logger
	params       GlanceParams
	namePrefix   string
	keepLatest   int
	sema         *semaphore.Weighted
	authenticate func(ctx context.Context, authURL string, opts gophercloud.AuthOptions) (*gophercloud.ProviderClient, error)
	listImages   func(ctx context.Context, provider *gophercloud.ProviderClient, region string) ([]images.Image, error)
}

// NewGlance constructs a Glance source using the real gophercloud client.
func NewGlance(params GlanceParams, log logr.Logger) (*Glance, error) {
	if params.AuthURLFormat == "" {
		return nil, errors.New("glance: authURLFormat is required")
	}
	if len(params.Regions) == 0 {
		return nil, errors.New("glance: at least one region is required")
	}

	prefix := params.NamePrefix
	if prefix == "" {
		prefix = defaultGlanceNamePrefix
	}

	keepLatest := params.KeepLatest
	if keepLatest == 0 {
		keepLatest = DefaultGlanceKeepLatest
	}

	parallel := params.Parallel
	if parallel <= 0 {
		parallel = defaultGlanceParallel
	}

	return &Glance{
		log:          log,
		params:       params,
		namePrefix:   prefix,
		keepLatest:   keepLatest,
		sema:         semaphore.NewWeighted(parallel),
		authenticate: defaultAuthenticate,
		listImages:   defaultListImages,
	}, nil
}

func defaultAuthenticate(ctx context.Context, authURL string, opts gophercloud.AuthOptions) (*gophercloud.ProviderClient, error) {
	opts.IdentityEndpoint = authURL
	provider, err := openstack.NewClient(authURL)
	if err != nil {
		return nil, err
	}

	provider.HTTPClient = http.Client{Timeout: glanceRequestTimeout}
	if err := openstack.Authenticate(ctx, provider, opts); err != nil {
		return nil, err
	}
	return provider, nil
}

func defaultListImages(ctx context.Context, provider *gophercloud.ProviderClient, region string) ([]images.Image, error) {
	client, err := openstack.NewImageV2(provider, gophercloud.EndpointOpts{Region: region})
	if err != nil {
		return nil, err
	}

	pages, err := images.List(client, images.ListOpts{
		Visibility: images.ImageVisibilityPublic,
		Limit:      1000,
	}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	return images.ExtractImages(pages)
}

func (g *Glance) authOptions() gophercloud.AuthOptions {
	return gophercloud.AuthOptions{
		Username:    g.params.Username,
		Password:    g.params.Password,
		DomainName:  g.params.UserDomainName,
		AllowReauth: true,
		Scope: &gophercloud.AuthScope{
			ProjectName: g.params.ProjectName,
			DomainName:  g.params.ProjectDomainName,
		},
	}
}

func (g *Glance) GetVersions(ctx context.Context) ([]ossync.SourceImage, error) {
	out := make(chan Result[[]ossync.SourceImage], len(g.params.Regions))
	for _, region := range g.params.Regions {
		go func() {
			if err := g.sema.Acquire(ctx, 1); err != nil {
				out <- Result[[]ossync.SourceImage]{err: err}
				return
			}
			defer g.sema.Release(1)
			found, err := g.discoverRegion(ctx, region)
			out <- Result[[]ossync.SourceImage]{value: found, err: err}
		}()
	}

	imagesByVersion := map[string]*ossync.SourceImage{}
	var skipped []error
	for range g.params.Regions {
		result := <-out
		if result.err != nil {
			if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
				return nil, result.err
			}
			skipped = append(skipped, result.err)
			continue
		}
		for _, img := range result.value {
			entry, exists := imagesByVersion[img.Version]
			if !exists {
				entry = &ossync.SourceImage{
					Version:       img.Version,
					Architectures: img.Architectures,
				}
				imagesByVersion[img.Version] = entry
			}
			entry.Regions = append(entry.Regions, img.Regions...)
		}
	}

	if len(skipped) > 0 {
		g.log.V(1).Info("skipped regions with errors", "count", len(skipped), "errors", errors.Join(skipped...))
	}
	if len(imagesByVersion) == 0 && len(skipped) == len(g.params.Regions) {
		return nil, fmt.Errorf("all %d regions failed: %w", len(g.params.Regions), errors.Join(skipped...))
	}

	versions := make([]ossync.SourceImage, 0, len(imagesByVersion))
	for _, img := range imagesByVersion {
		versions = append(versions, *img)
	}

	slices.SortFunc(versions, func(a, b ossync.SourceImage) int {
		return compareSemverDesc(a.Version, b.Version)
	})
	if g.keepLatest > 0 && len(versions) > g.keepLatest {
		versions = versions[:g.keepLatest]
	}

	supported := gardenerv1beta1.ClassificationSupported
	for i := range versions {
		versions[i].Classification = &supported
	}
	if len(versions) > 0 {
		deprecated := gardenerv1beta1.ClassificationDeprecated
		versions[len(versions)-1].Classification = &deprecated
	}

	return versions, nil
}

// discoverRegion returns a region's public images.
func (g *Glance) discoverRegion(ctx context.Context, region string) ([]ossync.SourceImage, error) {
	authURL := fmt.Sprintf(g.params.AuthURLFormat, region)
	provider, err := g.authenticate(ctx, authURL, g.authOptions())
	if err != nil {
		return nil, fmt.Errorf("region %s: authenticate: %w", region, err)
	}

	imgs, err := g.listImages(ctx, provider, region)
	if err != nil {
		return nil, fmt.Errorf("region %s: list images: %w", region, err)
	}

	// Pick one canonical image per version (a rebuilt image reuses the version with a new UUID).
	canonical := map[string]images.Image{}
	for _, img := range imgs {
		version, ok := g.parseVersion(img.Name)
		if !ok {
			continue
		}
		if cur, exists := canonical[version]; !exists || preferImage(img, cur) {
			canonical[version] = img
		}
	}

	found := make([]ossync.SourceImage, 0, len(canonical))
	for version, img := range canonical {
		found = append(found, ossync.SourceImage{
			Version:       version,
			Architectures: []string{"amd64"},
			Regions:       []ossync.RegionImage{{Region: region, ID: img.ID}},
		})
	}
	return found, nil
}

// preferImage reports whether candidate should replace current: active wins over non-active, then newer CreatedAt, then larger UUID (deterministic tiebreak).
func preferImage(candidate, current images.Image) bool {
	candActive := candidate.Status == images.ImageStatusActive
	curActive := current.Status == images.ImageStatusActive
	if candActive != curActive {
		return candActive
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return candidate.ID > current.ID
}

// compareSemverDesc orders two versions newest-first. Unparsable versions sort last.
func compareSemverDesc(a, b string) int {
	av, aerr := semver.ParseTolerant(a)
	bv, berr := semver.ParseTolerant(b)
	switch {
	case aerr != nil && berr != nil:
		return strings.Compare(a, b)
	case aerr != nil:
		return 1
	case berr != nil:
		return -1
	}
	return bv.Compare(av)
}

// parseVersion extracts the semver version from a matching image name.
func (g *Glance) parseVersion(name string) (string, bool) {
	if strings.Contains(name, usiVariantMarker) {
		g.log.V(1).Info("skipping usi image variant", "name", name)
		return "", false
	}

	rest, ok := strings.CutPrefix(name, g.namePrefix)
	if !ok {
		return "", false
	}

	// rest is "<version>-<hash>"; the hash is the final dash-separated segment.
	idx := strings.LastIndex(rest, "-")
	if idx <= 0 {
		return "", false
	}

	raw := rest[:idx]
	parsed, err := semver.ParseTolerant(raw)
	if err != nil {
		g.log.V(1).Info("skipping image with unparsable version", "name", name, "raw", raw)
		return "", false
	}
	return parsed.String(), true
}

// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

type Result[T any] struct {
	value T
	err   error
}

type SourceImage struct {
	Version       string
	Architectures []string
	CreatedAt     time.Time
}

type Source interface {
	GetVersions(ctx context.Context) ([]SourceImage, error)
}

type OCI struct {
	repo *remote.Repository
	sema *semaphore.Weighted
}

type OCIParams struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Username   string `json:"username"`
	Password   string `json:"password"` //nolint:gosec,nolintlint
	Parallel   int64  `json:"parallel"`
}

func NewOCI(params OCIParams, insecure bool) (*OCI, error) {
	// Create a new OCI repository
	repo, err := remote.NewRepository(params.Registry + "/" + params.Repository)
	if err != nil {
		return nil, err
	}

	if params.Username != "" && params.Password != "" {
		repo.Client = &auth.Client{
			Client: retry.DefaultClient,
			Cache:  auth.NewCache(),
			Credential: auth.StaticCredential(params.Registry, auth.Credential{
				Username: params.Username,
				Password: params.Password,
			}),
		}
	}
	repo.PlainHTTP = insecure

	return &OCI{
		repo: repo,
		sema: semaphore.NewWeighted(params.Parallel),
	}, nil
}

func (o *OCI) GetVersions(ctx context.Context) ([]SourceImage, error) {
	tags := []string{}
	err := o.repo.Tags(ctx, "", func(t []string) error {
		tags = append(tags, t...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make(chan Result[SourceImage])
	for _, tag := range tags {
		go func() {
			if err := o.sema.Acquire(ctx, 1); err != nil {
				out <- Result[SourceImage]{err: err}
				return
			}
			defer o.sema.Release(1)
			_, reader, err := o.repo.FetchReference(ctx, tag)
			if err != nil {
				out <- Result[SourceImage]{err: err}
				return
			}
			defer reader.Close()
			manifest := struct {
				Annotations map[string]string `json:"annotations"`
			}{}
			err = json.NewDecoder(reader).Decode(&manifest)
			if err != nil {
				out <- Result[SourceImage]{err: err}
				return
			}
			arch, ok := manifest.Annotations["architecture"]
			if !ok {
				out <- Result[SourceImage]{err: errors.New("architecture annotation not found in descriptor")}
				return
			}
			created := time.Time{}
			if s, ok := manifest.Annotations["org.opencontainers.image.created"]; ok {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					created = t
				}
			} else if s, ok := manifest.Annotations["created"]; ok {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					created = t
				}
			}
			out <- Result[SourceImage]{
				value: SourceImage{
					Version:       strings.ReplaceAll(tag, "_", "+"), // Follow the helm convention
					Architectures: []string{arch},
					CreatedAt:     created,
				},
			}
		}()
	}

	images := []SourceImage{}
	errs := []error{}
	for range tags {
		result := <-out
		if result.err != nil {
			errs = append(errs, result.err)
			continue
		}
		images = append(images, result.value)
	}
	return images, errors.Join(errs...)
}

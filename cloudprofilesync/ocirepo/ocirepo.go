// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package ocirepo

import (
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

type Params struct {
	Registry   string
	Repository string
	Username   string
	Password   string
	Insecure   bool
}

// New builds an oras-go remote repository with static-credential auth,
// shared by the OCI machine image source and the Keppel Kubernetes source.
func New(params Params) (*remote.Repository, error) {
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
	repo.PlainHTTP = params.Insecure

	return repo, nil
}

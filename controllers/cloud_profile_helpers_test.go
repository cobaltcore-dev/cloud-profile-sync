// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"errors"
	"testing"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
)

func TestGardenerVersionFromShoots_Found(t *testing.T) {
	shoots := []gardenerv1beta1.Shoot{
		{
			Status: gardenerv1beta1.ShootStatus{
				Gardener: gardenerv1beta1.Gardener{Version: "v1.150.2"},
			},
		},
	}
	ver, err := gardenerVersionFromShoots(shoots)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *ver != "v1.150.2" {
		t.Errorf("expected v1.150.2, got %q", *ver)
	}
}

func TestGardenerVersionFromShoots_MinVersion(t *testing.T) {
	shoots := []gardenerv1beta1.Shoot{
		{Status: gardenerv1beta1.ShootStatus{Gardener: gardenerv1beta1.Gardener{Version: "v1.151.0"}}},
		{Status: gardenerv1beta1.ShootStatus{Gardener: gardenerv1beta1.Gardener{Version: "v1.150.2"}}},
		{Status: gardenerv1beta1.ShootStatus{Gardener: gardenerv1beta1.Gardener{Version: "v1.152.0"}}},
	}
	ver, err := gardenerVersionFromShoots(shoots)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *ver != "v1.150.2" {
		t.Errorf("expected v1.150.2 (minimum), got %q", *ver)
	}
}

func TestGardenerVersionFromShoots_SkipsEmptyVersions(t *testing.T) {
	shoots := []gardenerv1beta1.Shoot{
		{Status: gardenerv1beta1.ShootStatus{}},
		{Status: gardenerv1beta1.ShootStatus{Gardener: gardenerv1beta1.Gardener{Version: "v1.150.2"}}},
	}
	ver, err := gardenerVersionFromShoots(shoots)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *ver != "v1.150.2" {
		t.Errorf("expected v1.150.2, got %q", *ver)
	}
}

func TestGardenerVersionFromShoots_NoShoots(t *testing.T) {
	_, err := gardenerVersionFromShoots(nil)
	if !errors.Is(err, ErrNoShoots) {
		t.Fatalf("expected ErrNoShoots, got %v", err)
	}
}

func TestGardenerVersionFromShoots_EmptyVersion(t *testing.T) {
	shoots := []gardenerv1beta1.Shoot{
		{Status: gardenerv1beta1.ShootStatus{}},
	}
	_, err := gardenerVersionFromShoots(shoots)
	if !errors.Is(err, ErrNoShoots) {
		t.Fatalf("expected ErrNoShoots, got %v", err)
	}
}

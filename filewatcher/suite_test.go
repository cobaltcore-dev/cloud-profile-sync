// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package filewatcher_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFilewatcher(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Filewatcher Suite")
}

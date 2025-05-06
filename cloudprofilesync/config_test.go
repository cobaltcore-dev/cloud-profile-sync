// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"encoding/json"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

const envVar = "CLOUDPROFILESYNC_TEST_VAR"

var _ = Describe("StringFromEnv", func() {

	BeforeEach(func() {
		Expect(os.Setenv(envVar, "abcdef")).To(Succeed())
	})

	AfterEach(func() {
		Expect(os.Unsetenv(envVar)).To(Succeed())
	})

	It("should unmarshal the literal value if not using env var syntax", func() {
		var str cloudprofilesync.StringFromEnv
		err := json.Unmarshal([]byte(`"literal"`), &str)
		Expect(err).To(Succeed())
		Expect(str).To(BeEquivalentTo("literal"))
	})

	It("should unmarhal the env var value if using env var syntax", func() {
		var str cloudprofilesync.StringFromEnv
		err := json.Unmarshal([]byte(`"${CLOUDPROFILESYNC_TEST_VAR}"`), &str)
		Expect(err).To(Succeed())
		Expect(str).To(BeEquivalentTo("abcdef"))
	})

	It("should return an error if the env var is not set", func() {
		Expect(os.Unsetenv(envVar)).To(Succeed())
		var str cloudprofilesync.StringFromEnv
		err := json.Unmarshal([]byte(`"${BANANA}"`), &str)
		Expect(err).To(HaveOccurred())
	})

})

// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type StringFromEnv string

func (s *StringFromEnv) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	if !strings.HasPrefix(str, "${") || !strings.HasSuffix(str, "}") {
		*s = StringFromEnv(str)
		return nil
	}
	envVar := strings.TrimPrefix(strings.TrimSuffix(str, "}"), "${")
	value, ok := os.LookupEnv(envVar)
	if !ok {
		return errors.New("environment variable not set: " + envVar)
	}
	*s = StringFromEnv(value)
	return nil
}

type ConfigDescriptor struct {
	CloudProfile string             `json:"cloudProfile"`
	Source       SourceDescriptor   `json:"source"`
	Provider     ProviderDescriptor `json:"provider"`
}

type SourceDescriptor struct {
	Name     string                  `json:"name"`
	OCI      *OCIDescriptor          `json:"oci"`
	Variants []VariantExtractionRule `json:"variants"`
}

type VariantExtractionRule struct {
	Regex             string `json:"regex"`
	ImageNameTemplate string `json:"imageNameTemplate"`
}

type OCIDescriptor struct {
	Registry   string        `json:"registry"`
	Repository string        `json:"repository"`
	Username   string        `json:"username"`
	Password   StringFromEnv `json:"password"`
	Parallel   int64         `json:"parallel"`
}

type IroncoreDescriptor struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	ImageName  string `json:"imageName"`
}

type ProviderDescriptor struct {
	Ironcore *IroncoreDescriptor `json:"ironcore"`
}

type Config struct {
	CloudProfile string
	Source       NamedSource
	Provider     Provider
}

type CompiledExtractor struct {
	Rule  VariantExtractionRule
	Regex *regexp.Regexp
}

func DefaultExtractor(name string) CompiledExtractor {
	re := `^(?P<version>.+)(?P<variant>)$`
	return CompiledExtractor{
		Rule:  VariantExtractionRule{Regex: re, ImageNameTemplate: name},
		Regex: regexp.MustCompile(re),
	}
}

func LoadConfig(data []byte) (Config, error) {
	var desc ConfigDescriptor
	if err := json.Unmarshal(data, &desc); err != nil {
		return Config{}, err
	}

	var source NamedSource
	source.Name = desc.Source.Name
	if desc.Source.OCI != nil {
		parallel := desc.Source.OCI.Parallel
		if parallel <= 0 {
			return Config{}, errors.New("parallel must be greater than 0")
		}
		oci, err := NewOCI(OCIParams{
			Registry:   desc.Source.OCI.Registry,
			Repository: desc.Source.OCI.Repository,
			Username:   desc.Source.OCI.Username,
			Password:   string(desc.Source.OCI.Password),
			Parallel:   desc.Source.OCI.Parallel,
		}, false)
		if err != nil {
			return Config{}, err
		}
		source.Source = oci
	} else {
		return Config{}, errors.New("no source defined")
	}

	var provider Provider
	if desc.Provider.Ironcore != nil {
		provider = &IroncoreProvider{
			Registry:   desc.Provider.Ironcore.Registry,
			Repository: desc.Provider.Ironcore.Repository,
			ImageName:  desc.Provider.Ironcore.ImageName,
		}
	} else {
		return Config{}, errors.New("no provider defined")
	}

	var extractors []CompiledExtractor
	if len(desc.Source.Variants) == 0 {
		extractors = append(extractors, DefaultExtractor(desc.Source.Name))
	} else {
		for _, r := range desc.Source.Variants {
			re, err := regexp.Compile(r.Regex)
			if err != nil {
				return Config{}, fmt.Errorf("invalid variant regex '%s': %w", r.Regex, err)
			}

			groups := re.SubexpNames()
			var hasVersion, hasVariant bool
			for _, g := range groups {
				if g == "version" {
					hasVersion = true
				}
				if g == "variant" {
					hasVariant = true
				}
			}
			if !hasVersion || !hasVariant {
				return Config{}, fmt.Errorf(
					"variant regex must contain named groups 'version' and 'variant': %s",
					r.Regex,
				)
			}
			extractors = append(extractors, CompiledExtractor{Rule: r, Regex: re})
		}
	}
	source.Extractors = extractors

	return Config{
		CloudProfile: desc.CloudProfile,
		Source:       source,
		Provider:     provider,
	}, nil
}

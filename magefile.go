// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

//go:build mage
// +build mage

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/pkg/errors"

	"github.com/elastic/beats/v7/dev-tools/mage"

	"github.com/elastic/apm-server/beater/config"
)

func init() {
	repo, err := mage.GetProjectRepoInfo()
	if err != nil {
		panic(err)
	}
	mage.SetBuildVariableSources(&mage.BuildVariableSources{
		BeatVersion: filepath.Join(repo.RootDir, "cmd", "version.go"),
		GoVersion:   filepath.Join(repo.RootDir, ".go-version"),
		DocBranch:   filepath.Join(repo.RootDir, "docs/version.asciidoc"),
	})

	mage.BeatDescription = "Elastic APM Server"
	mage.BeatURL = "https://www.elastic.co/apm"
	mage.BeatIndexPrefix = "apm"
	mage.XPackDir = "x-pack"
	mage.BeatUser = "apm-server"
	mage.CrossBuildMountModcache = true
}

// Build builds the Beat binary.
func Build() error {
	return mage.Build(mage.DefaultBuildArgs())
}

// GolangCrossBuild build the Beat binary inside of the golang-builder.
// Do not use directly, use crossBuild instead.
func GolangCrossBuild() error {
	return mage.GolangCrossBuild(mage.DefaultGolangCrossBuildArgs())
}

// BuildGoDaemon builds the go-daemon binary (use crossBuildGoDaemon).
func BuildGoDaemon() error {
	return mage.BuildGoDaemon()
}

// CrossBuild cross-builds the beat for all target platforms.
func CrossBuild() error {
	return mage.CrossBuild()
}

func CrossBuildXPack() error {
	return mage.CrossBuildXPack()
}

// CrossBuildGoDaemon cross-builds the go-daemon binary using Docker.
func CrossBuildGoDaemon() error {
	return mage.CrossBuildGoDaemon()
}

// Clean cleans all generated files and build artifacts.
func Clean() error {
	return mage.Clean()
}

func Config() error {
	if err := mage.Config(mage.ShortConfigType, shortConfigFileParams(), "."); err != nil {
		return err
	}
	return mage.Config(mage.DockerConfigType, dockerConfigFileParams(), ".")
}

func shortConfigFileParams() mage.ConfigFileParams {
	return mage.ConfigFileParams{
		Short: mage.ConfigParams{Template: mage.OSSBeatDir("_meta/beat.yml")},
		ExtraVars: map[string]interface{}{
			"elasticsearch_hostport": "localhost:9200",
			"listen_hostport":        "localhost:" + config.DefaultPort,
			"jaeger_grpc_hostport":   "localhost:14250",
			"jaeger_http_hostport":   "localhost:14268",
		},
	}
}

func dockerConfigFileParams() mage.ConfigFileParams {
	return mage.ConfigFileParams{
		Docker: mage.ConfigParams{Template: mage.OSSBeatDir("_meta/beat.yml")},
		ExtraVars: map[string]interface{}{
			"elasticsearch_hostport": "elasticsearch:9200",
			"listen_hostport":        "0.0.0.0:" + config.DefaultPort,
			"jaeger_grpc_hostport":   "0.0.0.0:14250",
			"jaeger_http_hostport":   "0.0.0.0:14268",
		},
	}
}

func keepPackages(types []string) map[mage.PackageType]struct{} {
	keep := make(map[mage.PackageType]struct{})
	for _, t := range types {
		var pt mage.PackageType
		if err := pt.UnmarshalText([]byte(t)); err != nil {
			log.Printf("skipped filtering package type %s", t)
			continue
		}
		keep[pt] = struct{}{}
	}
	return keep
}

func filterPackages(types string) {
	var packages []mage.OSPackageArgs
	keep := keepPackages(strings.Split(types, " "))
	for _, p := range mage.Packages {
		for _, t := range p.Types {
			if _, ok := keep[t]; !ok {
				continue
			}
			packages = append(packages, p)
			break
		}
	}
	mage.Packages = packages
}

// Package packages the Beat for distribution.
// Use SNAPSHOT=true to build snapshots.
// Use PLATFORMS to control the target platforms. eg linux/amd64
// Use TYPES to control the target types. eg docker
func Package() error {
	start := time.Now()
	defer func() { fmt.Println("package ran for", time.Since(start)) }()

	mage.UseElasticBeatPackaging()
	customizePackaging()

	if packageTypes := os.Getenv("TYPES"); packageTypes != "" {
		filterPackages(packageTypes)
	}

	if os.Getenv("SKIP_BUILD") != "true" {
		mg.Deps(Update)
		mg.Deps(CrossBuild, CrossBuildXPack, CrossBuildGoDaemon)
	}
	return mage.Package()
}

func Version() error {
	v, err := mage.BeatQualifiedVersion()
	if err != nil {
		return err
	}
	fmt.Print(v)
	return nil
}

// Update updates the generated files.
func Update() error {
	mg.Deps(Config)
	return nil
}

// Use RACE_DETECTOR=true to enable the race detector.
func GoTestUnit(ctx context.Context) error {
	return mage.GoTest(ctx, mage.DefaultGoTestUnitArgs())
}

// -----------------------------------------------------------------------------

func customizePackaging() {
	const emptyDir = "build/empty"
	if err := os.MkdirAll(emptyDir, 0750); err != nil {
		panic(errors.Wrapf(err, "failed to create dir %v", emptyDir))
	}

	for idx := len(mage.Packages) - 1; idx >= 0; idx-- {
		args := &mage.Packages[idx]
		pkgType := args.Types[0]
		if pkgType == mage.DMG {
			// We do not build macOS packages.
			mage.Packages = append(mage.Packages[:idx], mage.Packages[idx+1:]...)
			continue
		}

		// Replace the generic Beats README.md with an APM specific one, and remove files unused by apm-server.
		for filename, filespec := range args.Spec.Files {
			switch filespec.Source {
			case "{{ elastic_beats_dir }}/dev-tools/packaging/templates/common/README.md.tmpl":
				args.Spec.Files[filename] = mage.PackageFile{Mode: 0644, Template: "packaging/files/README.md.tmpl"}
			case "_meta/kibana.generated", "fields.yml", "{{.BeatName}}.reference.yml":
				delete(args.Spec.Files, filename)
			}
		}

		switch pkgType := args.Types[0]; pkgType {
		case mage.Zip, mage.TarGz:
			args.Spec.Files["java-attacher.jar"] = mage.PackageFile{Mode: 0750, Source: "build/java-attacher.jar", Owner: mage.BeatUser}

		case mage.Docker:
			args.Spec.ExtraVars["expose_ports"] = config.DefaultPort
			args.Spec.ExtraVars["repository"] = "docker.elastic.co/apm"
			args.Spec.Files["java-attacher.jar"] = mage.PackageFile{Mode: 0750, Source: "build/java-attacher.jar", Owner: mage.BeatUser}

		case mage.Deb, mage.RPM:
			// Update config file owner.
			pf := args.Spec.Files["/etc/{{.BeatName}}/{{.BeatName}}.yml"]
			pf.Owner = mage.BeatUser
			args.Spec.Files["/etc/{{.BeatName}}/{{.BeatName}}.yml"] = pf
			args.Spec.Files["/var/log/{{.BeatName}}"] = mage.PackageFile{Mode: 0750, Source: emptyDir, Owner: mage.BeatUser}
			args.Spec.Files["/usr/share/{{.BeatName}}/bin/java-attacher.jar"] = mage.PackageFile{Mode: 0750, Source: "build/java-attacher.jar", Owner: mage.BeatUser}

			// Customise the pre-install and post-install scripts.
			args.Spec.PreInstallScript = "packaging/files/linux/pre-install.sh.tmpl"
			if pkgType == mage.Deb {
				args.Spec.PostInstallScript = "packaging/files/linux/deb-post-install.sh.tmpl"
			}

		default:
			panic(errors.Errorf("unhandled package type: %v", pkgType))
		}
	}
}

// DumpVariables writes the template variables and values to stdout.
func DumpVariables() error {
	return mage.DumpVariables()
}

func Check() error {
	fmt.Println(">> check: Checking source code for common problems")

	mg.Deps(mage.GoVet, mage.CheckPythonTestNotExecutable, mage.CheckYAMLNotExecutable)

	changes, err := mage.GitDiffIndex()
	if err != nil {
		return errors.Wrap(err, "failed to diff the git index")
	}
	if len(changes) > 0 {
		if mg.Verbose() {
			mage.GitDiff()
		}
		return errors.Errorf("some files are not up-to-date. "+
			"Run 'make fmt update' then review and commit the changes. "+
			"Modified: %v", changes)
	}
	return nil
}

// PythonEnv ensures the Python venv is up-to-date with the beats requrements.txt.
func PythonEnv() error {
	_, err := mage.PythonVirtualenv()
	return err
}

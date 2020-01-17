package lifecycle_test

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apex/log"
	"github.com/apex/log/handlers/discard"
	"github.com/buildpacks/imgutil/fakes"
	"github.com/buildpacks/imgutil/local"
	"github.com/golang/mock/gomock"
	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"

	"github.com/buildpacks/lifecycle"
	"github.com/buildpacks/lifecycle/cache"
	"github.com/buildpacks/lifecycle/cmd"
	h "github.com/buildpacks/lifecycle/testhelpers"
	"github.com/buildpacks/lifecycle/testmock"
)

func TestAnalyzer(t *testing.T) {
	spec.Run(t, "Analyzer", testAnalyzer, spec.Report(report.Terminal{}))
}

//go:generate mockgen -mock_names Image=GGCRImage -package testmock -destination testmock/image.go github.com/google/go-containerregistry/pkg/v1 Image
//go:generate mockgen -package testmock -destination testmock/ref.go github.com/google/go-containerregistry/pkg/name Reference

func testAnalyzer(t *testing.T, when spec.G, it spec.S) {
	var (
		analyzer  *lifecycle.Analyzer
		mockCtrl  *gomock.Controller
		layerDir  string
		appDir    string
		tmpDir    string
		cacheDir  string
		testCache lifecycle.Cache
	)

	it.Before(func() {
		var err error

		tmpDir, err = ioutil.TempDir("", "analyzer-tests")
		h.AssertNil(t, err)

		layerDir, err = ioutil.TempDir("", "lifecycle-layer-dir")
		h.AssertNil(t, err)

		cacheDir, err = ioutil.TempDir("", "some-cache-dir")
		h.AssertNil(t, err)

		testCache, err = cache.NewVolumeCache(cacheDir)
		h.AssertNil(t, err)

		appDir = filepath.Join(layerDir, "some-app-dir")
		analyzer = &lifecycle.Analyzer{
			Buildpacks:   []lifecycle.Buildpack{{ID: "metadata.buildpack"}, {ID: "no.cache.buildpack"}, {ID: "no.metadata.buildpack"}},
			AppDir:       appDir,
			LayersDir:    layerDir,
			Logger:       &log.Logger{Handler: &discard.Handler{}},
			UID:          1234,
			GID:          4321,
		}
		if testing.Verbose() {
			analyzer.Logger = cmd.Logger
			cmd.SetLogLevel("debug")
		}
		mockCtrl = gomock.NewController(t)
	})

	it.After(func() {
		h.AssertNil(t, os.RemoveAll(tmpDir))
		h.AssertNil(t, os.RemoveAll(layerDir))
		h.AssertNil(t, os.RemoveAll(cacheDir))
		mockCtrl.Finish()
	})

	when("#Analyze", func() {
		var (
			image            *fakes.Image
			appImageMetadata lifecycle.LayersMetadata
			ref              *testmock.MockReference
		)

		it.Before(func() {
			image = fakes.NewImage("image-repo-name", "", local.IDIdentifier{
				ImageID: "s0m3D1g3sT",
			})
			ref = testmock.NewMockReference(mockCtrl)
			ref.EXPECT().Name().AnyTimes()
		})

		it.After(func() {
			image.Cleanup()
		})

		when("image exists", func() {
			it.Before(func() {
				metadata := h.MustReadFile(t, filepath.Join("testdata", "analyzer", "app_metadata.json"))
				h.AssertNil(t, image.SetLabel("io.buildpacks.lifecycle.metadata", string(metadata)))
				h.AssertNil(t, json.Unmarshal(metadata, &appImageMetadata))
			})

			it("restores layer metadata", func() {
				_, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)

				for _, data := range []struct{ name, want string }{
					{"metadata.buildpack/launch.toml", "[metadata]\n  launch-key = \"launch-value\""},
					{"metadata.buildpack/launch-build-cache.toml", "[metadata]\n  launch-build-cache-key = \"launch-build-cache-value\""},
					{"metadata.buildpack/launch-cache.toml", "[metadata]\n  launch-cache-key = \"launch-cache-value\""},
					{"no.cache.buildpack/some-layer.toml", "[metadata]\n  some-layer-key = \"some-layer-value\""},
				} {
					got := h.MustReadFile(t, filepath.Join(layerDir, data.name))
					h.AssertStringContains(t, string(got), data.want)
				}
			})

			it("restores layer sha files", func() {
				_, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)

				for _, data := range []struct{ name, want string }{
					{"metadata.buildpack/launch.sha", "launch-sha"},
					{"metadata.buildpack/launch-build-cache.sha", "launch-build-cache-sha"},
					{"metadata.buildpack/launch-cache.sha", "launch-cache-sha"},
					{"no.cache.buildpack/some-layer.sha", "some-layer-sha"},
				} {
					got := h.MustReadFile(t, filepath.Join(layerDir, data.name))
					h.AssertStringContains(t, string(got), data.want)
				}
			})

			it("does not restore launch=false layer metadata", func() {
				_, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)

				h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "launch-false.toml"))
				h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "launch-false.sha"))
			})

			it("does not restore build=true, cache=false layer metadata", func() {
				_, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)

				h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "launch-build.sha"))
			})

			when("subset of buildpacks are detected", func() {
				it.Before(func() {
					analyzer.Buildpacks = []lifecycle.Buildpack{{ID: "no.cache.buildpack"}}
				})
				it("restores layers for detected buildpacks", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					path := filepath.Join(layerDir, "no.cache.buildpack", "some-layer.toml")
					got := h.MustReadFile(t, path)
					want := "[metadata]\n  some-layer-key = \"some-layer-value\""

					h.AssertStringContains(t, string(got), want)
				})
				it("does not restore layers for undetected buildpacks", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack"))
				})
			})

			it("returns the analyzed metadata", func() {
				md, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)

				h.AssertEq(t, md.Image.Reference, "s0m3D1g3sT")
				h.AssertEq(t, md.Metadata, appImageMetadata)
			})

			when("cache exists", func() {
				it.Before(func() {
					metadata := h.MustReadFile(t, filepath.Join("testdata", "analyzer", "cache_metadata.json"))
					var cacheMetadata lifecycle.CacheMetadata
					h.AssertNil(t, json.Unmarshal(metadata, &cacheMetadata))
					h.AssertNil(t, testCache.SetMetadata(cacheMetadata))
					h.AssertNil(t, testCache.Commit())

					analyzer.Buildpacks = append(analyzer.Buildpacks, lifecycle.Buildpack{ID: "escaped/buildpack/id"})
				})

				it("restores app and cache layer metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					for _, data := range []struct{ name, want string }{
						// App layers.
						{"metadata.buildpack/launch.toml", "[metadata]\n  launch-key = \"launch-value\""},
						{"metadata.buildpack/launch-build-cache.toml", "[metadata]\n  launch-build-cache-key = \"launch-build-cache-value\""},
						{"metadata.buildpack/launch-cache.toml", "[metadata]\n  launch-cache-key = \"launch-cache-value\""},
						{"no.cache.buildpack/some-layer.toml", "[metadata]\n  some-layer-key = \"some-layer-value\""},
						// Cache-image-only layers.
						{"metadata.buildpack/cache.toml", "[metadata]\n  cache-key = \"cache-value\""},
					} {
						got := h.MustReadFile(t, filepath.Join(layerDir, data.name))
						h.AssertStringContains(t, string(got), data.want)
					}
				})

				it("restores app and cache layer sha files, prefers app sha", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					for _, data := range []struct{ name, want string }{
						{"metadata.buildpack/launch.sha", "launch-sha"},
						{"metadata.buildpack/launch-build-cache.sha", "launch-build-cache-sha"},
						{"metadata.buildpack/launch-cache.sha", "launch-cache-sha"},
						{"no.cache.buildpack/some-layer.sha", "some-layer-sha"},
						// Cache-image-only layers.
						{"metadata.buildpack/cache.sha", "cache-sha"},
					} {
						got := h.MustReadFile(t, filepath.Join(layerDir, data.name))
						h.AssertStringContains(t, string(got), data.want)
					}
				})

				it("does not overwrite metadata from app image", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					for _, name := range []string{
						"metadata.buildpack/launch-build-cache.toml",
						"metadata.buildpack/launch-cache.toml",
					} {
						got := h.MustReadFile(t, filepath.Join(layerDir, name))
						avoid := "[metadata]\n  cache-only-key = \"cache-only-value\""
						if strings.Contains(string(got), avoid) {
							t.Errorf("Expected %q to not contain %q, got %q", name, avoid, got)
						}
					}
				})

				it("does not overwrite sha from app image", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					for _, name := range []string{
						"metadata.buildpack/launch-build-cache.sha",
						"metadata.buildpack/launch-cache.sha",
					} {
						got := h.MustReadFile(t, filepath.Join(layerDir, name))
						avoid := "old-sha"
						if strings.Contains(string(got), avoid) {
							t.Errorf("Expected %q to not contain %q, got %q", name, avoid, got)
						}
					}
				})

				it("does not restore cache=true layers for non-selected groups", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "no.group.buildpack"))
				})

				it("does not restore launch=true layer metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "launch-cache-not-in-app.toml"))
					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "launch-cache-not-in-app.sha"))
				})

				it("does not restore cache=false layer metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "cache-false.toml"))
					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "cache-false.sha"))
				})

				it("restores escaped buildpack layer metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					path := filepath.Join(layerDir, "escaped_buildpack_id", "escaped-bp-layer.toml")
					got := h.MustReadFile(t, path)
					want := "[metadata]\n  escaped-bp-layer-key = \"escaped-bp-layer-value\""

					h.AssertStringContains(t, string(got), want)
				})

				when("subset of buildpacks are detected", func() {
					it.Before(func() {
						analyzer.Buildpacks = []lifecycle.Buildpack{{ID: "no.group.buildpack"}}
					})

					it("restores layers for detected buildpacks", func() {
						_, err := analyzer.Analyze(image, testCache)
						h.AssertNil(t, err)

						path := filepath.Join(layerDir, "no.group.buildpack", "some-layer.toml")
						got := h.MustReadFile(t, path)
						want := "[metadata]\n  some-layer-key = \"some-layer-value\""

						h.AssertStringContains(t, string(got), want)
					})
					it("does not restore layers for undetected buildpacks", func() {
						_, err := analyzer.Analyze(image, testCache)
						h.AssertNil(t, err)

						h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack"))
						h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "escaped_buildpack_id"))
					})
				})
			})

			when("analyzer is running as root", func() {
				it.Before(func() {
					if os.Getuid() != 0 {
						t.Skip("Skipped when not running as root")
					}
				})

				it("chowns new files to CNB_USER_ID:CNB_GROUP_ID", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)
					h.AssertUIDGID(t, layerDir, 1234, 4321)
					h.AssertUIDGID(t, filepath.Join(layerDir, "metadata.buildpack", "launch.toml"), 1234, 4321)
					h.AssertUIDGID(t, filepath.Join(layerDir, "no.cache.buildpack"), 1234, 4321)
					h.AssertUIDGID(t, filepath.Join(layerDir, "no.cache.buildpack", "some-layer.toml"), 1234, 4321)
				})
			})

			when("skip-layers is true", func() {
				it.Before(func() {
					analyzer.SkipLayers = true
				})

				it("should return the analyzed metadata", func() {
					md, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertEq(t, md.Image.Reference, "s0m3D1g3sT")
					h.AssertEq(t, md.Metadata, appImageMetadata)
				})

				it("does not write buildpack layer metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					files, err := ioutil.ReadDir(layerDir)
					h.AssertNil(t, err)
					h.AssertEq(t, len(files), 0)
				})
			})
		})

		when("image is not found", func() {
			it.Before(func() {
				h.AssertNil(t, image.Delete())
			})

			when("cache exists", func() {
				it.Before(func() {
					metadata := h.MustReadFile(t, filepath.Join("testdata", "analyzer", "cache_metadata.json"))
					var cacheMetadata lifecycle.CacheMetadata
					h.AssertNil(t, json.Unmarshal(metadata, &cacheMetadata))
					h.AssertNil(t, testCache.SetMetadata(cacheMetadata))
					h.AssertNil(t, testCache.Commit())

					analyzer.Buildpacks = append(analyzer.Buildpacks, lifecycle.Buildpack{ID: "escaped/buildpack/id"})
				})

				it("restores cache=true layer metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					for _, data := range []struct{ name, want string }{
						{"metadata.buildpack/cache.toml", "[metadata]\n  cache-key = \"cache-value\""},
					} {
						got := h.MustReadFile(t, filepath.Join(layerDir, data.name))
						h.AssertStringContains(t, string(got), data.want)
					}
				})

				it("does not restore launch=true layer metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "launch-cache.toml"))
					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "launch-build-cache.toml"))
					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "launch-cache-not-in-app.toml"))
				})

				it("does not restore cache=false layer metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertPathDoesNotExist(t, filepath.Join(layerDir, "metadata.buildpack", "cache-false.toml"))
				})

				it("returns a nil image in the analyzed metadata", func() {
					md, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertNil(t, md.Image)
					h.AssertEq(t, md.Metadata, lifecycle.LayersMetadata{})
				})
			})
			when("cache is empty", func() {
				it("does not restore any metadata", func() {
					_, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					files, err := ioutil.ReadDir(layerDir)
					h.AssertNil(t, err)
					h.AssertEq(t, len(files), 0)
				})
				it("returns a nil image in the analyzed metadata", func() {
					md, err := analyzer.Analyze(image, testCache)
					h.AssertNil(t, err)

					h.AssertNil(t, md.Image)
					h.AssertEq(t, md.Metadata, lifecycle.LayersMetadata{})
				})
			})
			when("cache is not provided", func() {
				it("does not restore any metadata", func() {
					_, err := analyzer.Analyze(image, nil)
					h.AssertNil(t, err)

					files, err := ioutil.ReadDir(layerDir)
					h.AssertNil(t, err)
					h.AssertEq(t, len(files), 0)
				})
				it("returns a nil image in the analyzed metadata", func() {
					md, err := analyzer.Analyze(image, nil)
					h.AssertNil(t, err)

					h.AssertNil(t, md.Image)
					h.AssertEq(t, md.Metadata, lifecycle.LayersMetadata{})
				})
			})
		})

		when("image does not have metadata label", func() {
			it.Before(func() {
				h.AssertNil(t, image.SetLabel("io.buildpacks.lifecycle.metadata", ""))
			})
			it("does not restore any metadata", func() {
				_, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)

				files, err := ioutil.ReadDir(layerDir)
				h.AssertNil(t, err)
				h.AssertEq(t, len(files), 0)
			})
			it("returns empty analyzed metadata", func() {
				md, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)
				h.AssertEq(t, md.Metadata, lifecycle.LayersMetadata{})
			})
		})

		when("image has incompatible metadata", func() {
			it.Before(func() {
				h.AssertNil(t, image.SetLabel("io.buildpacks.lifecycle.metadata", `{["bad", "metadata"]}`))
			})
			it("does not restore any metadata", func() {
				_, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)

				files, err := ioutil.ReadDir(layerDir)
				h.AssertNil(t, err)
				h.AssertEq(t, len(files), 0)
			})
			it("returns empty analyzed metadata", func() {
				md, err := analyzer.Analyze(image, testCache)
				h.AssertNil(t, err)
				h.AssertEq(t, md.Metadata, lifecycle.LayersMetadata{})
			})
		})
	})
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/buildpacks/imgutil"
	"github.com/buildpacks/imgutil/local"
	"github.com/buildpacks/imgutil/remote"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/buildpacks/lifecycle"
	"github.com/buildpacks/lifecycle/auth"
	"github.com/buildpacks/lifecycle/cache"
	"github.com/buildpacks/lifecycle/cmd"
	"github.com/buildpacks/lifecycle/image"
)

var (
	imageName      string
	orderPath      string
	runImageRef    string
	layersDir      string
	appDir         string
	platformDir    string
	stackPath      string
	launchCacheDir string
	launcherPath   string
	buildpacksDir  string
	useDaemon      bool
	useHelpers     bool
	uid            int
	gid            int
	printVersion   bool
	logLevel       string
	cacheImageTag  string
	cacheDir       string
	skipLayers     bool //TODO rename me
)

func init() {
	cmd.FlagRunImage(&runImageRef)
	cmd.FlagLayersDir(&layersDir)
	cmd.FlagAppDir(&appDir)
	cmd.FlagBuildpacksDir(&buildpacksDir)
	cmd.FlagStackPath(&stackPath)
	cmd.FlagLaunchCacheDir(&launchCacheDir)
	cmd.FlagUseDaemon(&useDaemon)
	cmd.FlagOrderPath(&orderPath)
	cmd.FlagPlatformDir(&platformDir)
	cmd.FlagUseCredHelpers(&useHelpers)
	cmd.FlagUID(&uid)
	cmd.FlagGID(&gid)
	cmd.FlagVersion(&printVersion)
	cmd.FlagLauncherPath(&launcherPath)
	cmd.FlagLogLevel(&logLevel)
	cmd.FlagCacheImage(&cacheImageTag)
	cmd.FlagCacheDir(&cacheDir)
	cmd.FlagSkipLayers(&skipLayers)
}

func main() {
	flag.Parse()

	// suppress output from libraries, lifecycle will not use standard logger
	log.SetOutput(ioutil.Discard)

	if printVersion {
		cmd.ExitWithVersion()
	}

	if err := cmd.SetLogLevel(logLevel); err != nil {
		cmd.Exit(err)
	}

	if args := flag.Args(); len(args) < 1 || args[0] == "" {
		cmd.Exit(cmd.FailErrCode(errors.New("at least one image argument is required"), cmd.CodeInvalidArgs, "parse arguments"))
	}

	imageName = flag.Arg(0)

	if launchCacheDir != "" && !useDaemon {
		cmd.Exit(cmd.FailErrCode(errors.New("launch cache can only be used when exporting to a Docker daemon"), cmd.CodeInvalidArgs, "parse arguments"))
	}

	cmd.Exit(all())
}

func all() error {
	start := time.Now()
	//cpupprofpath := filepath.Join(cacheDir, "cpu.prof")
	//fmt.Println("writing cpu profile to", cpupprofpath)
	//f, err := os.Create(cpupprofpath)
	//if err != nil {
	//	panic(err)
	//}
	//pprof.StartCPUProfile(f)

	defer func() {
		//pprof.StopCPUProfile()
		//f.Close()
		//cmd.Logger.Info("closed file and stopped pprof")
		cmd.Logger.Infof("all in %f s", float64(time.Now().Sub(start).Milliseconds())/1000.0)
	}()

	if useHelpers {
		if err := lifecycle.SetupCredHelpers(filepath.Join(os.Getenv("HOME"), ".docker"), imageName); err != nil {
			return cmd.FailErr(err, "setup credential helpers")
		}
	}

	env := &lifecycle.Env{
		LookupEnv: os.LookupEnv,
		Getenv:    os.Getenv,
		Setenv:    os.Setenv,
		Unsetenv:  os.Unsetenv,
		Environ:   os.Environ,
		Map:       lifecycle.POSIXBuildEnv,
	}

	fullEnv, err := env.WithPlatform(platformDir)
	if err != nil {
		return cmd.FailErr(err, "read full env")
	}

	var cacheStore lifecycle.Cache
	if cacheImageTag != "" {
		cacheStore, err = cache.NewImageCacheFromName(cacheImageTag, auth.EnvKeychain(cmd.EnvRegistryAuth))
		if err != nil {
			return cmd.FailErr(err, "create image cache")
		}
	} else if cacheDir != "" {
		cacheStore, err = cache.NewVolumeCache(cacheDir)
		if err != nil {
			return cmd.FailErr(err, "create volume cache")
		}
	}

	artifactsDir, err := ioutil.TempDir("", "lifecycle.exporter.layer")
	if err != nil {
		return cmd.FailErr(err, "create temp directory")
	}
	defer os.RemoveAll(artifactsDir)

	order, err := lifecycle.ReadOrder(orderPath)
	if err != nil {
		return cmd.FailErr(err, "read buildpack order file")
	}

	detect := time.Now()
	group, plan, err := order.Detect(&lifecycle.DetectConfig{
		FullEnv:       fullEnv,
		ClearEnv:      env.List(),
		AppDir:        appDir,
		PlatformDir:   platformDir,
		BuildpacksDir: buildpacksDir,
		Logger:        cmd.Logger,
	})
	cmd.Logger.Infof("detected in %f s", float64(time.Now().Sub(detect).Milliseconds())/1000.0)
	if err != nil {
		if err == lifecycle.ErrFail {
			cmd.Logger.Error("No buildpack groups passed detection.")
			cmd.Logger.Error("Please check that you are running against the correct path.")
		}
		return cmd.FailErrCode(err, cmd.CodeFailedDetect, "detect")
	}

	analyzer := &lifecycle.Analyzer{
		Buildpacks: group.Group,
		AppDir:     appDir,
		LayersDir:  layersDir,
		Logger:     cmd.Logger,
		UID:        uid,
		GID:        gid,
		SkipLayers: skipLayers,
	}

	restorer := &lifecycle.Restorer{
		LayersDir:  layersDir,
		Buildpacks: group.Group,
		Logger:     cmd.Logger,
		UID:        uid,
		GID:        gid,
	}

	builder := &lifecycle.Builder{
		AppDir:        appDir,
		LayersDir:     layersDir,
		PlatformDir:   platformDir,
		BuildpacksDir: buildpacksDir,
		Env:           env,
		Group:         group,
		Plan:          plan,
		Out:           log.New(os.Stdout, "", 0),
		Err:           log.New(os.Stderr, "", 0),
	}

	exporter := &lifecycle.Exporter{
		Buildpacks:   group.Group,
		Logger:       cmd.Logger,
		UID:          uid,
		GID:          gid,
		ArtifactsDir: artifactsDir,
	}

	var img imgutil.Image

	if useDaemon {
		dockerClient, err := cmd.DockerClient()
		if err != nil {
			return cmd.FailErr(err, "create docker client")
		}
		img, err = local.NewImage(
			imageName,
			dockerClient,
			local.FromBaseImage(imageName),
		)
		if err != nil {
			return cmd.FailErr(err, "access previous image")
		}
	} else {
		img, err = remote.NewImage(
			imageName,
			auth.EnvKeychain(cmd.EnvRegistryAuth),
			remote.FromBaseImage(imageName),
		)
		if err != nil {
			return cmd.FailErr(err, "access previous image")
		}
	}
	analyze := time.Now()
	analyzedMd, err := analyzer.Analyze(img, cacheStore)
	if err != nil {
		return cmd.FailErrCode(err, cmd.CodeFailed, "analyze")
	}
	cmd.Logger.Infof("analyzed in %f s", float64(time.Now().Sub(analyze).Milliseconds())/1000.0)

	restore := time.Now()
	if err = restorer.Restore(cacheStore); err != nil {
		return cmd.FailErrCode(err, cmd.CodeFailed, "restore")
	}
	cmd.Logger.Infof("restored in %f s", float64(time.Now().Sub(restore).Milliseconds())/1000.0)

	build := time.Now()
	buildMd, err := builder.Build()
	if err != nil {
		return cmd.FailErrCode(err, cmd.CodeFailedBuild, "build")
	}
	cmd.Logger.Infof("built in %f s", float64(time.Now().Sub(build).Milliseconds())/1000.0)

	if err := lifecycle.WriteTOML(lifecycle.MetadataFilePath(layersDir), buildMd); err != nil {
		return cmd.FailErr(err, "write metadata")
	}

	var registry string
	var appImage imgutil.Image
	var runImageID imgutil.Identifier
	var stackMD lifecycle.StackMetadata

	if registry, err = image.EnsureSingleRegistry(imageName); err != nil {
		return cmd.FailErrCode(err, cmd.CodeInvalidArgs, "parse arguments")
	}

	_, err = toml.DecodeFile(stackPath, &stackMD)
	if err != nil {
		cmd.Logger.Infof("no stack metadata found at path '%s', stack metadata will not be exported\n", stackPath)
	}

	if runImageRef == "" {
		if stackMD.RunImage.Image == "" {
			return cmd.FailErrCode(errors.New("-image is required when there is no stack metadata available"), cmd.CodeInvalidArgs, "parse arguments")
		}

		runImageRef, err = stackMD.BestRunImageMirror(registry)
		if err != nil {
			return err
		}
	}

	if useDaemon {
		dockerClient, err := cmd.DockerClient()
		if err != nil {
			return err
		}

		var opts = []local.ImageOption{
			local.FromBaseImage(runImageRef),
		}

		if analyzedMd.Image != nil {
			cmd.Logger.Debugf("Reusing layers from image with id '%s'", analyzedMd.Image.Reference)
			opts = append(opts, local.WithPreviousImage(analyzedMd.Image.Reference))
		}

		appImage, err = local.NewImage(
			imageName,
			dockerClient,
			opts...,
		)
		if err != nil {
			return cmd.FailErr(err, "access run image")
		}

		runImageID, err = appImage.Identifier()
		if err != nil {
			return cmd.FailErr(err, "get run image ID")
		}

		if launchCacheDir != "" {
			volumeCache, err := cache.NewVolumeCache(launchCacheDir)
			if err != nil {
				return cmd.FailErr(err, "create launch cache")
			}
			appImage = cache.NewCachingImage(appImage, volumeCache)
		}
	} else {
		var opts = []remote.ImageOption{
			remote.FromBaseImage(runImageRef),
		}

		if analyzedMd.Image != nil {
			cmd.Logger.Infof("Reusing layers from image '%s'", analyzedMd.Image.Reference)
			ref, err := name.ParseReference(analyzedMd.Image.Reference, name.WeakValidation)
			if err != nil {
				return cmd.FailErr(err, "parse analyzed registry")
			}
			analyzedRegistry := ref.Context().RegistryStr()
			if analyzedRegistry != registry {
				return fmt.Errorf("analyzed image is on a different registry %s from the exported image %s", analyzedRegistry, registry)
			}
			opts = append(opts, remote.WithPreviousImage(analyzedMd.Image.Reference))
		}

		appImage, err = remote.NewImage(
			imageName,
			auth.EnvKeychain(cmd.EnvRegistryAuth),
			opts...,
		)
		if err != nil {
			return cmd.FailErr(err, "access run image")
		}

		runImage, err := remote.NewImage(runImageRef, auth.EnvKeychain(cmd.EnvRegistryAuth), remote.FromBaseImage(runImageRef))
		if err != nil {
			return cmd.FailErr(err, "access run image")
		}
		runImageID, err = runImage.Identifier()
		if err != nil {
			return cmd.FailErr(err, "get run image reference")
		}
	}

	launcherConfig := lifecycle.LauncherConfig{
		Path: launcherPath,
		Metadata: lifecycle.LauncherMetadata{
			Version: cmd.Version,
			Source: lifecycle.SourceMetadata{
				Git: lifecycle.GitMetadata{
					Repository: cmd.SCMRepository,
					Commit:     cmd.SCMCommit,
				},
			},
		},
	}

	export := time.Now()
	if err := exporter.Export(layersDir, appDir, appImage, runImageID.String(), analyzedMd.Metadata, []string{}, launcherConfig, stackMD); err != nil {
		if _, isSaveError := err.(*imgutil.SaveError); isSaveError {
			return cmd.FailErrCode(err, cmd.CodeFailedSave, "export")
		}
		return cmd.FailErr(err, "export")
	}

	cached := time.Now()
	err = exporter.Cache(layersDir, cacheStore)
	cmd.Logger.Infof("cached in %f s", float64(time.Now().Sub(cached).Milliseconds())/1000.0)
	cmd.Logger.Infof("exported in %f s", float64(time.Now().Sub(export).Milliseconds())/1000.0)

	// Failing to export cache should not be an error if the app image export was successful.
	return err
}

//
//func exportCache(exporter *lifecycle.Exporter) error {
//	var err error
//	var cacheStore lifecycle.Cache
//	switch {
//	case cacheImageTag != "":
//		cacheStore, err = cache.NewImageCacheFromName(cacheImageTag, auth.EnvKeychain(cmd.EnvRegistryAuth))
//		if err != nil {
//			return cmd.FailErr(err, "create image cache")
//		}
//	case cacheDir != "":
//		cacheStore, err = cache.NewVolumeCache(cacheDir)
//		if err != nil {
//			return cmd.FailErr(err, "create volume cache")
//		}
//	default:
//		exporter.Logger.Warn("Not exporting cache: no cache flag specified.")
//		return nil
//	}
//	return exporter.Cache(layersDir, cacheStore)
//}

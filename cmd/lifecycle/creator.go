package main

import (
	"fmt"

	"github.com/docker/docker/client"

	"github.com/buildpacks/lifecycle/cmd"
	"github.com/buildpacks/lifecycle/docker"
	"github.com/buildpacks/lifecycle/image"
)

type createCmd struct {
	//flags: inputs
	appDir              string
	buildpacksDir       string
	cacheDir            string
	cacheImageTag       string
	imageName           string
	launchCacheDir      string
	launcherPath        string
	layersDir           string
	orderPath           string
	platformDir         string
	previousImage       string
	runImageRef         string
	stackPath           string
	uid, gid            int
	additionalTags      cmd.StringSlice
	skipRestore         bool
	useDaemon           bool
	projectMetadataPath string
	processType         string

	//set if necessary before dropping privileges
	docker client.CommonAPIClient
}

func (c *createCmd) Init() {
	cmd.FlagAppDir(&c.appDir)
	cmd.FlagBuildpacksDir(&c.buildpacksDir)
	cmd.FlagCacheDir(&c.cacheDir)
	cmd.FlagCacheImage(&c.cacheImageTag)
	cmd.FlagGID(&c.gid)
	cmd.FlagLaunchCacheDir(&c.launchCacheDir)
	cmd.FlagLauncherPath(&c.launcherPath)
	cmd.FlagLayersDir(&c.layersDir)
	cmd.FlagOrderPath(&c.orderPath)
	cmd.FlagPlatformDir(&c.platformDir)
	cmd.FlagPreviousImage(&c.previousImage)
	cmd.FlagRunImage(&c.runImageRef)
	cmd.FlagSkipRestore(&c.skipRestore)
	cmd.FlagStackPath(&c.stackPath)
	cmd.FlagUID(&c.uid)
	cmd.FlagUseDaemon(&c.useDaemon)
	cmd.FlagTags(&c.additionalTags)
	cmd.FlagProjectMetadataPath(&c.projectMetadataPath)
	cmd.FlagProcessType(&c.processType)
}

func (c *createCmd) Args(nargs int, args []string) error {
	if nargs != 1 {
		return cmd.FailErrCode(fmt.Errorf("received %d arguments, but expected 1", nargs), cmd.CodeInvalidArgs, "parse arguments")
	}

	c.imageName = args[0]
	if c.launchCacheDir != "" && !c.useDaemon {
		cmd.Logger.Warn("Ignoring -launch-cache, only intended for use with -daemon")
		c.launchCacheDir = ""
	}

	if c.cacheImageTag == "" && c.cacheDir == "" {
		cmd.Logger.Warn("Not restoring cached layer data, no cache flag specified.")
	}

	if c.previousImage == "" {
		c.previousImage = c.imageName
	}

	if err := image.EnsureSingleRegistry(append(c.additionalTags, c.imageName)...); err != nil {
		return cmd.FailErrCode(err, cmd.CodeInvalidArgs, "all tags must have the same registry as the exported image")
	}

	return nil
}

func (c *createCmd) Privileges() error {
	if c.useDaemon {
		var err error
		c.docker, err = docker.Client()
		if err != nil {
			return cmd.FailErr(err, "initialize docker client")
		}
	}
	if err := cmd.EnsureOwner(c.uid, c.gid, c.cacheDir, c.launchCacheDir, c.layersDir); err != nil {
		return cmd.FailErr(err, "chown volumes")
	}
	if err := cmd.RunAs(c.uid, c.gid); err != nil {
		cmd.FailErr(err, fmt.Sprintf("exec as user %d:%d", c.uid, c.gid))
	}
	return nil
}

func (c *createCmd) Exec() error {
	cacheStore, err := initCache(c.cacheImageTag, c.cacheDir)
	if err != nil {
		return err
	}

	cmd.Logger.Info("---> DETECTING")
	group, plan, err := detectArgs{
		buildpacksDir: c.buildpacksDir,
		appDir:        c.appDir,
		platformDir:   c.platformDir,
		orderPath:     c.orderPath,
	}.detect()
	if err != nil {
		return cmd.FailErrCode(err, cmd.CodeFailed, "detect")
	}

	cmd.Logger.Info("---> ANALYZING")
	analyzedMD, err := analyzeArgs{
		imageName:  c.previousImage,
		layersDir:  c.layersDir,
		skipLayers: c.skipRestore,
		useDaemon:  c.useDaemon,
		docker:     c.docker,
	}.analyze(group, cacheStore)
	if err != nil {
		return err
	}

	if !c.skipRestore {
		cmd.Logger.Info("---> RESTORING")
		if err := restore(c.layersDir, group, cacheStore); err != nil {
			return err
		}
	}

	cmd.Logger.Info("---> BUILDING")
	err = buildArgs{
		buildpacksDir: c.buildpacksDir,
		layersDir:     c.layersDir,
		appDir:        c.appDir,
		platformDir:   c.platformDir,
	}.build(group, plan)
	if err != nil {
		return err
	}

	cmd.Logger.Info("---> EXPORTING")
	return exportArgs{
		stackPath:           c.stackPath,
		imageNames:          append([]string{c.imageName}, c.additionalTags...),
		launchCacheDir:      c.launchCacheDir,
		appDir:              c.appDir,
		layersDir:           c.layersDir,
		launcherPath:        c.launcherPath,
		projectMetadataPath: c.projectMetadataPath,
		runImageRef:         c.runImageRef,
		useDaemon:           c.useDaemon,
		uid:                 c.uid,
		gid:                 c.gid,
		processType:         c.processType,
		docker:              c.docker,
	}.export(group, cacheStore, analyzedMD)
}

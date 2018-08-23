package main

import (
	"flag"
	"io/ioutil"
	"log"
	"os"

	"github.com/buildpack/packs"

	"github.com/buildpack/lifecycle"
	"fmt"
)

var (
	buildpackPath string
	orderPath     string
	groupPath     string
	infoPath      string
)

func init() {
	packs.InputBPPath(&buildpackPath)
	packs.InputBPOrderPath(&orderPath)

	packs.InputBPGroupPath(&groupPath)
	packs.InputDetectInfoPath(&infoPath)
}

func main() {
	flag.Parse()
	if flag.NArg() != 0 || buildpackPath == "" || orderPath == "" || groupPath == "" || infoPath == "" {
		packs.Exit(packs.FailCode(packs.CodeInvalidArgs, "parse arguments"))
	}
	packs.Exit(detect())
}

func detect() error {
	logger := log.New(os.Stderr, "", log.LstdFlags)

	buildpacks, err := lifecycle.NewBuildpackMap(buildpackPath)
	if err != nil {
		return packs.FailErr(err, "read buildpack directory")
	}
	order, err := buildpacks.ReadOrder(orderPath)
	if err != nil {
		return packs.FailErr(err, "read buildpack order file")
	}

	fmt.Printf("detecting\n")
	info, group := order.Detect(logger, lifecycle.DefaultAppDir)
	if group == nil {
		fmt.Printf("info: %+v\n", info)
		return packs.FailCode(packs.CodeFailedDetect, "detect")
	}
	fmt.Printf("group: %+v\n", group)

	if err := group.Write(groupPath); err != nil {
		return packs.FailErr(err, "write buildpack group")
	}

	if err := ioutil.WriteFile(infoPath, info, 0666); err != nil {
		return packs.FailErr(err, "write detect info")
	}

	return nil
}

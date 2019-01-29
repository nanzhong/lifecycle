package image

import (
	"os"

	"github.com/docker/docker/client"
	"github.com/pkg/errors"

	"github.com/buildpack/lifecycle/fs"
)

type Factory struct {
	Docker *client.Client
	FS     *fs.FS
	Out    *os.File
}

func DefaultFactory(outputFile *os.File) (*Factory, error) {
	f := &Factory{
		FS: &fs.FS{},
	}

	var err error
	f.Docker, err = newDocker()
	if err != nil {
		return nil, err
	}

	f.Out = outputFile

	return f, nil
}

func newDocker() (*client.Client, error) {
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithVersion("1.38"))
	if err != nil {
		return nil, errors.Wrap(err, "new docker client")
	}
	return docker, nil
}

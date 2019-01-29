package image

import (
	"io"
	"io/ioutil"

	"github.com/docker/docker/client"
	"github.com/pkg/errors"

	"github.com/buildpack/lifecycle/fs"
)

type Factory struct {
	Docker *client.Client
	FS     *fs.FS
	Out    io.Writer
}

func NewFactory(ops ...func(*Factory)) (*Factory, error) {
	f := &Factory{
		FS:  &fs.FS{},
		Out: ioutil.Discard,
	}

	var err error
	f.Docker, err = newDocker()
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		op(f)
	}

	return f, nil
}

func WithOutWriter(w io.Writer) func(factory *Factory) {
	return func(factory *Factory) {
		factory.Out = w
	}
}

func newDocker() (*client.Client, error) {
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithVersion("1.38"))
	if err != nil {
		return nil, errors.Wrap(err, "new docker client")
	}
	return docker, nil
}

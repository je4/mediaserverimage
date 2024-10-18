package main

import (
	"emperror.dev/errors"
	"github.com/BurntSushi/toml"
	"github.com/je4/filesystem/v3/pkg/vfsrw"
	"github.com/je4/utils/v2/pkg/config"
	"github.com/je4/utils/v2/pkg/stashconfig"
	"go.ub.unibas.ch/cloud/certloader/v2/pkg/loader"
	"io/fs"
	"os"
)

type MediaserverImageConfig struct {
	LocalAddr               string                `toml:"localaddr"`
	Instance                string                `toml:"instance"`
	Domains                 []string              `toml:"domains"`
	ResolverAddr            string                `toml:"resolveraddr"`
	ResolverTimeout         config.Duration       `toml:"resolvertimeout"`
	ResolverNotFoundTimeout config.Duration       `toml:"resolvernotfoundtimeout"`
	Server                  loader.Config         `toml:"server"`
	Client                  loader.Config         `toml:"client"`
	GRPCClient              map[string]string     `toml:"grpcclient"`
	VFS                     map[string]*vfsrw.VFS `toml:"vfs"`
	Concurrency             uint32                `toml:"concurrency"`
	QueueSize               uint32                `toml:"queuesize"`
	Log                     stashconfig.Config    `toml:"log"`
}

func LoadMediaserverImageConfig(fSys fs.FS, fp string, conf *MediaserverImageConfig) error {
	if _, err := fs.Stat(fSys, fp); err != nil {
		path, err := os.Getwd()
		if err != nil {
			return errors.Wrap(err, "cannot get current working directory")
		}
		fSys = os.DirFS(path)
		fp = "mediaserverimage.toml"
	}
	data, err := fs.ReadFile(fSys, fp)
	if err != nil {
		return errors.Wrapf(err, "cannot read file [%v] %s", fSys, fp)
	}
	_, err = toml.Decode(string(data), conf)
	if err != nil {
		return errors.Wrapf(err, "error loading config file %v", fp)
	}
	return nil
}

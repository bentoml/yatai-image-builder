package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"

	"github.com/urfave/cli/v2"

	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	"github.com/containerd/containerd/v2/contrib/snapshotservice"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	sddaemon "github.com/coreos/go-systemd/v22/daemon"
	"google.golang.org/grpc"

	"github.com/bentoml/yatai-image-builder/bento-image-snapshotter/snapshot"
)

type Config struct {
	RootPath string `toml:"root_path"`
}

func init() {
	fmt.Println("Registering bento snapshotter")
	registry.Register(&plugin.Registration{
		Type:   plugins.SnapshotPlugin,
		ID:     "bento",
		Config: &Config{},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			fmt.Println("Initializing bento snapshotter")
			cfg, ok := ic.Config.(*Config)
			if !ok {
				return nil, errors.New("invalid bento snapshotter configuration")
			}

			root := ic.Properties[plugins.PropertyRootDir]
			if root == "" {
				cfg.RootPath = root
			}

			rs, err := snapshot.NewSnapshotter(ic.Context, cfg.RootPath)
			if err != nil {
				return nil, errors.Wrap(err, "failed to initialize snapshotter")
			}
			return rs, nil
		},
	})
	fmt.Println("Bento snapshotter registered")
}

func main() {
	app := &cli.App{
		Name:  "bento-image-snapshotter",
		Usage: "Run a bento-image snapshotter",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "root-dir",
				Value: snapshot.DefaultRootDir,
			},
			&cli.StringFlag{
				Name:  "socket-addr",
				Value: snapshot.DefaultSocketAddress,
			},
		},
		Action: func(ctx *cli.Context) error {
			ctx_ := ctx.Context
			root := ctx.String("root-dir")
			sn, err := snapshot.NewSnapshotter(ctx_, root)
			if err != nil {
				return errors.Wrap(err, "failed to create snapshotter")
			}
			service := snapshotservice.FromSnapshotter(sn)
			rpc := grpc.NewServer()
			snapshotsapi.RegisterSnapshotsServer(rpc, service)
			socksPath := ctx.String("socket-addr")
			log.G(ctx_).Infof("socket path: %s", socksPath)
			// Prepare the directory for the socket
			if err := os.MkdirAll(filepath.Dir(socksPath), 0700); err != nil {
				return errors.Wrapf(err, "failed to create directory: %s", filepath.Dir(socksPath))
			}
			err = os.RemoveAll(socksPath)
			if err != nil {
				return errors.Wrapf(err, "failed to remove socket: %s", socksPath)
			}
			l, err := net.Listen("unix", socksPath)
			if err != nil {
				return errors.Wrapf(err, "failed to listen on socket: %s", socksPath)
			}
			errCh := make(chan error, 1)
			go func() {
				if err := rpc.Serve(l); err != nil {
					errCh <- errors.Wrap(err, "failed to serve")
				}
			}()

			if os.Getenv("NOTIFY_SOCKET") != "" {
				notified, notifyErr := sddaemon.SdNotify(false, sddaemon.SdNotifyReady)
				log.G(ctx_).Debugf("SdNotifyReady notified=%v, err=%v", notified, notifyErr)
			}
			defer func() {
				if os.Getenv("NOTIFY_SOCKET") != "" {
					notified, notifyErr := sddaemon.SdNotify(false, sddaemon.SdNotifyStopping)
					log.G(ctx_).Debugf("SdNotifyStopping notified=%v, err=%v", notified, notifyErr)
				}
			}()

			var s os.Signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, unix.SIGINT, unix.SIGTERM)
			select {
			case s = <-sigCh:
				log.G(ctx_).Infof("Got %v", s)
			case err := <-errCh:
				return err
			}
			return nil
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.G(context.Background()).WithError(err).Fatal("failed to run")
	}
}

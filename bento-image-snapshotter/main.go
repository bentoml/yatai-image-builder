package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/jessevdk/go-flags"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"

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

type runOptions struct {
	RootDir             string `short:"r" long:"root-dir" description:"Root directory for the snapshotter" required:"false"`
	SocketAddr          string `short:"s" long:"socket-addr" description:"Socket address for the snapshotter" required:"false"`
	Debug               bool   `short:"d" long:"debug" description:"Enable debug logging" required:"false"`
	DownloadConcurrency int    `short:"c" long:"download-concurrency" description:"Number of concurrent downloads" required:"false" default:"32" env:"DOWNLOAD_CONCURRENCY"`
	DownloadPartSizeMiB int64  `short:"p" long:"download-part-size" description:"Size of each download part, in MiB" required:"false" default:"32" env:"DOWNLOAD_PART_SIZE"`
}

func run(ctx context.Context, opts *runOptions) error {
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	root := opts.RootDir
	if root == "" {
		root = snapshot.DefaultRootDir
	}
	sn, err := snapshot.NewSnapshotter(ctx, root, snapshot.WithDownloadConcurrency(opts.DownloadConcurrency), snapshot.WithDownloadPartSize(opts.DownloadPartSizeMiB*1024*1024))
	if err != nil {
		return errors.Wrap(err, "failed to create snapshotter")
	}
	service := snapshotservice.FromSnapshotter(sn)
	rpc := grpc.NewServer()
	snapshotsapi.RegisterSnapshotsServer(rpc, service)
	socksAddr := opts.SocketAddr
	if socksAddr == "" {
		socksAddr = snapshot.DefaultSocketAddress
	}
	log.G(ctx).Infof("socket path: %s", socksAddr)
	// Prepare the directory for the socket
	if err := os.MkdirAll(filepath.Dir(socksAddr), 0700); err != nil {
		return errors.Wrapf(err, "failed to create directory: %s", filepath.Dir(socksAddr))
	}
	err = os.RemoveAll(socksAddr)
	if err != nil {
		return errors.Wrapf(err, "failed to remove socket: %s", socksAddr)
	}
	l, err := net.Listen("unix", socksAddr)
	if err != nil {
		return errors.Wrapf(err, "failed to listen on socket: %s", socksAddr)
	}
	errCh := make(chan error, 1)
	go func() {
		if err := rpc.Serve(l); err != nil {
			errCh <- errors.Wrap(err, "failed to serve")
		}
	}()

	if os.Getenv("NOTIFY_SOCKET") != "" {
		notified, notifyErr := sddaemon.SdNotify(false, sddaemon.SdNotifyReady)
		log.G(ctx).Debugf("SdNotifyReady notified=%v, err=%v", notified, notifyErr)
	}
	defer func() {
		if os.Getenv("NOTIFY_SOCKET") != "" {
			notified, notifyErr := sddaemon.SdNotify(false, sddaemon.SdNotifyStopping)
			log.G(ctx).Debugf("SdNotifyStopping notified=%v, err=%v", notified, notifyErr)
		}
	}()

	var s os.Signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, unix.SIGINT, unix.SIGTERM)
	select {
	case s = <-sigCh:
		log.G(ctx).Infof("Got %v", s)
		cancel()
	case err := <-errCh:
		return err
	}
	return nil
}

func main() {
	ctx := context.Background()

	var opts runOptions

	_, err := flags.Parse(&opts)
	if err != nil {
		os.Exit(1)
	}

	if opts.Debug {
		_ = log.SetLevel("debug")
	}

	err = run(ctx, &opts)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

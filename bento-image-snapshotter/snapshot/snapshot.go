/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/storage"
	"github.com/containerd/containerd/v2/plugins/snapshots/overlay/overlayutils"
	"github.com/containerd/continuity/fs"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	stargzfs "github.com/containerd/stargz-snapshotter/fs"
	stargzfsconfig "github.com/containerd/stargz-snapshotter/fs/config"
	"github.com/moby/sys/mountinfo"
	"golang.org/x/sync/errgroup"

	"github.com/pkg/errors"

	ourfs "github.com/bentoml/yatai-image-builder/bento-image-snapshotter/fs"
	"github.com/bentoml/yatai-image-builder/bento-image-snapshotter/fs/stargzs3"
	"github.com/bentoml/yatai-image-builder/common"
)

const (
	targetSnapshotLabel = "containerd.io/snapshot.ref"
	remoteLabel         = "containerd.io/snapshot/remote"
	remoteLabelVal      = "remote snapshot"

	// remoteSnapshotLogKey is a key for log line, which indicates whether
	// `Prepare` method successfully prepared targeting remote snapshot or not, as
	// defined in the following:
	// - "true"  : indicates the snapshot has been successfully prepared as a
	//             remote snapshot
	// - "false" : indicates the snapshot failed to be prepared as a remote
	//             snapshot
	// - null    : undetermined
	remoteSnapshotLogKey = "remote-snapshot-prepared"
	prepareSucceeded     = "true"
	prepareFailed        = "false"
)

// FileSystem is a backing filesystem abstraction.
//
// Mount() tries to mount a remote snapshot to the specified mount point
// directory. If succeed, the mountpoint directory will be treated as a layer
// snapshot. If Mount() fails, the mountpoint directory MUST be cleaned up.
// Check() is called to check the connectibity of the existing layer snapshot
// every time the layer is used by containerd.
// Unmount() is called to unmount a remote snapshot from the specified mount point
// directory.
type FileSystem interface {
	Mount(ctx context.Context, mountpoint string, labels map[string]string) error
	Check(ctx context.Context, mountpoint string, labels map[string]string) error
	Unmount(ctx context.Context, mountpoint string) error
}

// SnapshotterConfig is used to configure the remote snapshotter instance
type SnapshotterConfig struct {
	asyncRemove                 bool
	noRestore                   bool
	allowInvalidMountsOnRestart bool
	downloadConcurrency         int
	downloadPartSize            int64
}

// Opt is an option to configure the remote snapshotter
type Opt func(config *SnapshotterConfig) error

// AsynchronousRemove defers removal of filesystem content until
// the Cleanup method is called. Removals will make the snapshot
// referred to by the key unavailable and make the key immediately
// available for re-use.
func AsynchronousRemove(config *SnapshotterConfig) error {
	config.asyncRemove = true
	return nil
}

func NoRestore(config *SnapshotterConfig) error {
	config.noRestore = true
	return nil
}

func AllowInvalidMountsOnRestart(config *SnapshotterConfig) error {
	config.allowInvalidMountsOnRestart = true
	return nil
}

func WithDownloadConcurrency(concurrency int) Opt {
	return func(config *SnapshotterConfig) error {
		config.downloadConcurrency = concurrency
		return nil
	}
}

func WithDownloadPartSize(partSize int64) Opt {
	return func(config *SnapshotterConfig) error {
		config.downloadPartSize = partSize
		return nil
	}
}

type snapshotter struct {
	root        string
	ms          *storage.MetaStore
	asyncRemove bool

	// s3fs is a filesystem that this snapshotter recognizes.
	s3fs                        FileSystem
	stargzs3fs                  FileSystem
	userxattr                   bool // whether to enable "userxattr" mount option
	noRestore                   bool
	allowInvalidMountsOnRestart bool
}

// NewSnapshotter returns a Snapshotter which can use unpacked remote layers
// as snapshots. This is implemented based on the overlayfs snapshotter, so
// diffs are stored under the provided root and a metadata file is stored under
// the root as same as overlayfs snapshotter.
func NewSnapshotter(ctx context.Context, root string, opts ...Opt) (snapshots.Snapshotter, error) {
	var config SnapshotterConfig
	for _, opt := range opts {
		if err := opt(&config); err != nil {
			return nil, errors.Wrap(err, "failed to apply option")
		}
	}

	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.Wrap(err, "failed to create mountpoint")
	}
	supportsDType, err := fs.SupportsDType(root)
	if err != nil {
		return nil, errors.Wrap(err, "failed to check filesystem support for d_type")
	}
	if !supportsDType {
		return nil, errors.Errorf("%s does not support d_type. If the backing filesystem is xfs, please reformat with ftype=1 to enable d_type support", root)
	}
	ms, err := storage.NewMetaStore(filepath.Join(root, "metadata.db"))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create metadata store")
	}

	if err := os.Mkdir(filepath.Join(root, "snapshots"), 0700); err != nil && !os.IsExist(err) {
		return nil, errors.Wrap(err, "failed to create mountpoint")
	}

	userxattr, err := overlayutils.NeedsUserXAttr(root)
	if err != nil {
		log.G(ctx).WithError(err).Warnf("cannot detect whether \"userxattr\" option needs to be used, assuming to be %v", userxattr)
	}

	s3fs := ourfs.NewS3FileSystem()
	stargzs3fs, err := stargzfs.NewFilesystem(root, stargzfsconfig.Config{
		HTTPCacheType:       "memory",
		FSCacheType:         "memory",
		PrefetchSize:        32 * 1024 * 1024, // 32MB
		DisableVerification: true,
		NoBackgroundFetch:   true,
	}, stargzfs.WithResolveHandler("stargzs3", stargzs3.NewResolveHandler(config.downloadConcurrency, config.downloadPartSize)))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create stargz filesystem")
	}

	o := &snapshotter{
		root:                        root,
		ms:                          ms,
		asyncRemove:                 config.asyncRemove,
		s3fs:                        s3fs,
		stargzs3fs:                  stargzs3fs,
		userxattr:                   userxattr,
		noRestore:                   config.noRestore,
		allowInvalidMountsOnRestart: config.allowInvalidMountsOnRestart,
	}

	log.G(ctx).Info("restoring remote snapshot...")
	if err := o.restoreRemoteSnapshot(ctx); err != nil {
		log.G(ctx).WithError(err).Warn("failed to restore remote snapshot")
	} else {
		log.G(ctx).Info("restored remote snapshot")
	}

	return o, nil
}

// Stat returns the info for an active or committed snapshot by name or
// key.
//
// Should be used for parent resolution, existence checks and to discern
// the kind of snapshot.
func (o *snapshotter) Stat(ctx context.Context, key string) (snapshots.Info, error) {
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return snapshots.Info{}, errors.Wrap(err, "failed to get transaction")
	}
	defer func() {
		_ = t.Rollback()
	}()
	_, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return snapshots.Info{}, errors.Wrap(err, "failed to get info")
	}

	return info, nil
}

func (o *snapshotter) Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error) {
	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return snapshots.Info{}, errors.Wrap(err, "failed to get transaction")
	}

	info, err = storage.UpdateInfo(ctx, info, fieldpaths...)
	if err != nil {
		_ = t.Rollback()
		return snapshots.Info{}, errors.Wrap(err, "failed to update info")
	}

	if err := t.Commit(); err != nil {
		return snapshots.Info{}, errors.Wrap(err, "failed to commit transaction")
	}

	return info, nil
}

// Usage returns the resources taken by the snapshot identified by key.
//
// For active snapshots, this will scan the usage of the overlay "diff" (aka
// "upper") directory and may take some time.
// for remote snapshots, no scan will be held and recognize the number of inodes
// and these sizes as "zero".
//
// For committed snapshots, the value is returned from the metadata database.
func (o *snapshotter) Usage(ctx context.Context, key string) (snapshots.Usage, error) {
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return snapshots.Usage{}, errors.Wrap(err, "failed to get transaction")
	}
	id, info, usage, err := storage.GetInfo(ctx, key)
	_ = t.Rollback() // transaction no longer needed at this point.

	if err != nil {
		return snapshots.Usage{}, errors.Wrap(err, "failed to get info")
	}

	upperPath := o.upperPath(id)

	if info.Kind == snapshots.KindActive {
		du, err := fs.DiskUsage(ctx, upperPath)
		if err != nil {
			// TODO(stevvooe): Consider not reporting an error in this case.
			return snapshots.Usage{}, errors.Wrap(err, "failed to get disk usage")
		}

		usage = snapshots.Usage(du)
	}

	return usage, nil
}

func (o *snapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	s, err := o.createSnapshot(ctx, snapshots.KindActive, key, parent, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create snapshot")
	}

	// Try to prepare the remote snapshot. If succeeded, we commit the snapshot now
	// and return ErrAlreadyExists.
	var base snapshots.Info
	for _, opt := range opts {
		if err := opt(&base); err != nil {
			return nil, errors.Wrap(err, "failed to apply option")
		}
	}
	bucketName := base.Labels[common.DescriptorAnnotationBucket]
	if target, ok := base.Labels[targetSnapshotLabel]; ok && bucketName != "" {
		// NOTE: If passed labels include a target of the remote snapshot, `Prepare`
		//       must log whether this method succeeded to prepare that remote snapshot
		//       or not, using the key `remoteSnapshotLogKey` defined in the above. This
		//       log is used by tests in this project.
		lCtx := log.WithLogger(ctx, log.G(ctx).WithField("key", key).WithField("parent", parent))
		if err := o.prepareRemoteSnapshot(lCtx, key, base.Labels); err != nil {
			log.G(lCtx).WithField(remoteSnapshotLogKey, prepareFailed).
				WithError(err).Warn("failed to prepare remote snapshot")
		} else {
			base.Labels[remoteLabel] = remoteLabelVal // Mark this snapshot as remote
			err := o.commit(ctx, true, target, key, append(opts, snapshots.WithLabels(base.Labels))...)
			if err == nil || errdefs.IsAlreadyExists(err) {
				// count also AlreadyExists as "success"
				log.G(lCtx).WithField(remoteSnapshotLogKey, prepareSucceeded).Debug("prepared remote snapshot")
				return nil, errors.Wrapf(errdefs.ErrAlreadyExists, "target snapshot %q already exists", target)
				// return nil, fmt.Errorf("target snapshot %q: %w", target, errdefs.ErrAlreadyExists)
			}
			log.G(lCtx).WithField(remoteSnapshotLogKey, prepareFailed).
				WithError(err).Warn("failed to internally commit remote snapshot")
			// Don't fallback here (= prohibit to use this key again) because the FileSystem
			// possible has done some work on this "upper" directory.
			return nil, errors.Wrap(err, "failed to commit snapshot")
		}
	}
	return o.mounts(ctx, s, parent)
}

func (o *snapshotter) View(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	s, err := o.createSnapshot(ctx, snapshots.KindView, key, parent, opts)
	if err != nil {
		return nil, err
	}
	return o.mounts(ctx, s, parent)
}

// Mounts returns the mounts for the transaction identified by key. Can be
// called on an read-write or readonly transaction.
//
// This can be used to recover mounts after calling View or Prepare.
func (o *snapshotter) Mounts(ctx context.Context, key string) ([]mount.Mount, error) {
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get transaction")
	}
	s, err := storage.GetSnapshot(ctx, key)
	_ = t.Rollback()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get active snapshot")
	}
	return o.mounts(ctx, s, key)
}

func (o *snapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error {
	return o.commit(ctx, false, name, key, opts...)
}

func (o *snapshotter) commit(ctx context.Context, isRemote bool, name, key string, opts ...snapshots.Opt) error {
	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return errors.Wrap(err, "failed to get transaction")
	}

	rollback := true
	defer func() {
		if rollback {
			if rerr := t.Rollback(); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
			}
		}
	}()

	// grab the existing id
	id, _, usage, err := storage.GetInfo(ctx, key)
	if err != nil {
		return errors.Wrap(err, "failed to get info")
	}

	if !isRemote { // skip diskusage for remote snapshots for allowing lazy preparation of nodes
		du, err := fs.DiskUsage(ctx, o.upperPath(id))
		if err != nil {
			return errors.Wrap(err, "failed to get disk usage")
		}
		usage = snapshots.Usage(du)
	}

	if _, err = storage.CommitActive(ctx, key, name, usage, opts...); err != nil {
		return errors.Wrap(err, "failed to commit snapshot")
	}

	rollback = false
	return errors.Wrap(t.Commit(), "failed to commit transaction")
}

// Remove abandons the snapshot identified by key. The snapshot will
// immediately become unavailable and unrecoverable. Disk space will
// be freed up on the next call to `Cleanup`.
func (o *snapshotter) Remove(ctx context.Context, key string) (err error) {
	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return errors.Wrap(err, "failed to get transaction")
	}
	defer func() {
		if err != nil {
			if rerr := t.Rollback(); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
			}
		}
	}()

	_, _, err = storage.Remove(ctx, key)
	if err != nil {
		return errors.Wrap(err, "failed to remove")
	}

	if !o.asyncRemove {
		var removals []string
		const cleanupCommitted = false
		removals, err = o.getCleanupDirectories(ctx, cleanupCommitted)
		if err != nil {
			return errors.Wrap(err, "failed to get directories for removal")
		}

		// Remove directories after the transaction is closed, failures must not
		// return error since the transaction is committed with the removal
		// key no longer available.
		defer func() {
			if err == nil {
				for _, dir := range removals {
					if err := o.cleanupSnapshotDirectory(ctx, dir); err != nil {
						log.G(ctx).WithError(err).WithField("path", dir).Warn("failed to remove directory")
					}
				}
			}
		}()
	}

	return errors.Wrap(t.Commit(), "failed to commit transaction")
}

// Walk the snapshots.
func (o *snapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, fs ...string) error {
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return errors.Wrap(err, "failed to get transaction")
	}
	defer func() {
		_ = t.Rollback()
	}()
	return errors.Wrap(storage.WalkInfo(ctx, fn, fs...), "failed to walk info")
}

// Cleanup cleans up disk resources from removed or abandoned snapshots
func (o *snapshotter) Cleanup(ctx context.Context) error {
	const cleanupCommitted = false
	return o.cleanup(ctx, cleanupCommitted)
}

func (o *snapshotter) cleanup(ctx context.Context, cleanupCommitted bool) error {
	cleanup, err := o.cleanupDirectories(ctx, cleanupCommitted)
	if err != nil {
		return err
	}

	log.G(ctx).Debugf("cleanup: dirs=%v", cleanup)
	for _, dir := range cleanup {
		if err := o.cleanupSnapshotDirectory(ctx, dir); err != nil {
			log.G(ctx).WithError(err).WithField("path", dir).Warn("failed to remove directory")
		}
	}

	return nil
}

func (o *snapshotter) cleanupDirectories(ctx context.Context, cleanupCommitted bool) ([]string, error) {
	// Get a write transaction to ensure no other write transaction can be entered
	// while the cleanup is scanning.
	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get transaction")
	}

	defer func() {
		_ = t.Rollback()
	}()
	return o.getCleanupDirectories(ctx, cleanupCommitted)
}

func (o *snapshotter) getCleanupDirectories(ctx context.Context, cleanupCommitted bool) ([]string, error) {
	ids, err := storage.IDMap(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ID map")
	}

	snapshotDir := filepath.Join(o.root, "snapshots")
	fd, err := os.Open(snapshotDir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open snapshot directory")
	}
	defer fd.Close()

	dirs, err := fd.Readdirnames(0)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read snapshot directory")
	}

	cleanup := []string{}
	for _, d := range dirs {
		if !cleanupCommitted {
			if _, ok := ids[d]; ok {
				continue
			}
		}

		cleanup = append(cleanup, filepath.Join(snapshotDir, d))
	}

	return cleanup, nil
}

func (o *snapshotter) cleanupSnapshotDirectory(ctx context.Context, dir string) error {
	// On a remote snapshot, the layer is mounted on the "fs" directory.
	// We use Filesystem's Unmount API so that it can do necessary finalization
	// before/after the unmount.
	mp := filepath.Join(dir, "fs")
	hasMountPoint := false
	_, err := mountinfo.GetMounts(func(m *mountinfo.Info) (skip, stop bool) {
		if strings.HasPrefix(m.Mountpoint, mp) {
			hasMountPoint = true
			if err := o.stargzs3fs.Unmount(ctx, m.Mountpoint); err != nil {
				log.G(ctx).WithError(err).WithField("dir", dir).Debug("failed to unmount")
			}
			return true, true
		}
		return false, false
	})
	if err != nil {
		return errors.Wrap(err, "failed to get mounts")
	}
	if !hasMountPoint {
		if err := o.s3fs.Unmount(ctx, mp); err != nil {
			log.G(ctx).WithError(err).WithField("dir", mp).Debug("failed to unmount")
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return errors.Wrapf(err, "failed to remove directory %q", dir)
	}
	return nil
}

func (o *snapshotter) createSnapshot(ctx context.Context, kind snapshots.Kind, key, parent string, opts []snapshots.Opt) (_ storage.Snapshot, err error) {
	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return storage.Snapshot{}, errors.Wrap(err, "failed to get transaction")
	}

	var td, path string
	defer func() {
		if err != nil {
			if td != "" {
				if err1 := o.cleanupSnapshotDirectory(ctx, td); err1 != nil {
					log.G(ctx).WithError(err1).Warn("failed to cleanup temp snapshot directory")
				}
			}
			if path != "" {
				if err1 := o.cleanupSnapshotDirectory(ctx, path); err1 != nil {
					log.G(ctx).WithError(err1).WithField("path", path).Error("failed to reclaim snapshot directory, directory may need removal")
					err = errors.Wrapf(err, "failed to remove path: %v", err1)
				}
			}
		}
	}()

	snapshotDir := filepath.Join(o.root, "snapshots")
	td, err = o.prepareDirectory(snapshotDir, kind)
	if err != nil {
		if rerr := t.Rollback(); rerr != nil {
			log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
		}
		return storage.Snapshot{}, errors.Wrap(err, "failed to create prepare snapshot dir")
	}
	rollback := true
	defer func() {
		if rollback {
			if rerr := t.Rollback(); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
			}
		}
	}()

	s, err := storage.CreateSnapshot(ctx, kind, key, parent, opts...)
	if err != nil {
		return storage.Snapshot{}, errors.Wrap(err, "failed to create snapshot")
	}

	if len(s.ParentIDs) > 0 {
		st, err := os.Stat(o.upperPath(s.ParentIDs[0]))
		if err != nil {
			return storage.Snapshot{}, errors.Wrap(err, "failed to stat parent")
		}

		stat := st.Sys().(*syscall.Stat_t)

		if err := os.Lchown(filepath.Join(td, "fs"), int(stat.Uid), int(stat.Gid)); err != nil {
			if rerr := t.Rollback(); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
			}
			return storage.Snapshot{}, errors.Wrap(err, "failed to chown")
		}
	}

	path = filepath.Join(snapshotDir, s.ID)
	if err = os.Rename(td, path); err != nil {
		return storage.Snapshot{}, errors.Wrap(err, "failed to rename")
	}
	td = ""

	rollback = false
	if err = t.Commit(); err != nil {
		return storage.Snapshot{}, errors.Wrap(err, "commit failed")
	}

	return s, nil
}

func (o *snapshotter) prepareDirectory(snapshotDir string, kind snapshots.Kind) (string, error) {
	td, err := os.MkdirTemp(snapshotDir, "new-")
	if err != nil {
		return "", errors.Wrap(err, "failed to create temp dir")
	}

	if err := os.Mkdir(filepath.Join(td, "fs"), 0755); err != nil {
		return td, errors.Wrap(err, "failed to create fs directory")
	}

	if kind == snapshots.KindActive {
		if err := os.Mkdir(filepath.Join(td, "work"), 0711); err != nil {
			return td, errors.Wrap(err, "failed to create work directory")
		}
	}

	return td, nil
}

func (o *snapshotter) mounts(ctx context.Context, s storage.Snapshot, checkKey string) ([]mount.Mount, error) {
	// Make sure that all layers lower than the target layer are available
	if checkKey != "" && !o.checkAvailability(ctx, checkKey) {
		return nil, errors.Wrapf(errdefs.ErrUnavailable, "layer %q unavailable", s.ID)
	}

	if len(s.ParentIDs) == 0 {
		// if we only have one layer/no parents then just return a bind mount as overlay
		// will not work
		roFlag := "rw"
		if s.Kind == snapshots.KindView {
			roFlag = "ro"
		}

		return []mount.Mount{
			{
				Source: o.upperPath(s.ID),
				Type:   "bind",
				Options: []string{
					roFlag,
					"rbind",
				},
			},
		}, nil
	}
	var options []string

	if s.Kind == snapshots.KindActive {
		options = append(options,
			"workdir="+o.workPath(s.ID),
			"upperdir="+o.upperPath(s.ID),
		)
	} else if len(s.ParentIDs) == 1 {
		return []mount.Mount{
			{
				Source: o.upperPath(s.ParentIDs[0]),
				Type:   "bind",
				Options: []string{
					"ro",
					"rbind",
				},
			},
		}, nil
	}

	parentPaths := make([]string, len(s.ParentIDs))
	for i := range s.ParentIDs {
		parentPaths[i] = o.upperPath(s.ParentIDs[i])
	}

	options = append(options, "lowerdir="+strings.Join(parentPaths, ":"))
	if o.userxattr {
		options = append(options, "userxattr")
	}
	return []mount.Mount{
		{
			Type:    "overlay",
			Source:  "overlay",
			Options: options,
		},
	}, nil
}

func (o *snapshotter) upperPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "fs")
}

func (o *snapshotter) workPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "work")
}

// Close closes the snapshotter
func (o *snapshotter) Close() error {
	// unmount all mounts including Committed
	const cleanupCommitted = true
	ctx := context.Background()
	if err := o.cleanup(ctx, cleanupCommitted); err != nil {
		log.G(ctx).WithError(err).Warn("failed to cleanup")
	}
	return errors.Wrap(o.ms.Close(), "failed to close metadata store")
}

// prepareRemoteSnapshot tries to prepare the snapshot as a remote snapshot
// using filesystems registered in this snapshotter.
func (o *snapshotter) prepareRemoteSnapshot(ctx context.Context, key string, labels map[string]string) error {
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return errors.Wrap(err, "failed to get transaction")
	}
	defer func() {
		_ = t.Rollback()
	}()
	id, _, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return errors.Wrap(err, "failed to get info")
	}

	mountpoint := o.upperPath(id)
	log.G(ctx).Infof("preparing filesystem mount at mountpoint=%v", mountpoint)

	format := labels[common.DescriptorAnnotationFormat]
	if format == common.DescriptorAnnotationValueFormatStargz {
		return errors.Wrap(o.stargzs3fs.Mount(ctx, mountpoint, labels), "failed to mount stargzs3 filesystem")
	}

	return errors.Wrap(o.s3fs.Mount(ctx, mountpoint, labels), "failed to mount s3 filesystem")
}

// checkAvailability checks avaiability of the specified layer and all lower
// layers using filesystem's checking functionality.
func (o *snapshotter) checkAvailability(ctx context.Context, key string) bool {
	log.G(ctx).WithField("key", key).Debug("checking layer availability")

	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		log.G(ctx).WithError(err).Warn("failed to get transaction")
		return false
	}
	defer func() {
		_ = t.Rollback()
	}()

	eg, egCtx := errgroup.WithContext(ctx)
	for cKey := key; cKey != ""; {
		id, info, _, err := storage.GetInfo(ctx, cKey)
		if err != nil {
			log.G(ctx).WithError(err).Warnf("failed to get info of %q", cKey)
			return false
		}
		mp := o.upperPath(id)
		lCtx := log.WithLogger(ctx, log.G(ctx).WithField("mount-point", mp))
		if _, ok := info.Labels[remoteLabel]; ok {
			eg.Go(func() error {
				log.G(lCtx).Debug("checking mount point")
				fs := o.s3fs
				if info.Labels[common.DescriptorAnnotationFormat] == common.DescriptorAnnotationValueFormatStargz {
					fs = o.stargzs3fs
				}
				if err := fs.Check(egCtx, mp, info.Labels); err != nil {
					log.G(lCtx).WithError(err).Warn("layer is unavailable")
					return errors.Wrap(err, "failed to check layer")
				}
				return nil
			})
		} else {
			log.G(lCtx).Debug("layer is normal snapshot(overlayfs)")
		}
		cKey = info.Parent
	}
	if err := eg.Wait(); err != nil {
		return false
	}
	return true
}

func (o *snapshotter) restoreRemoteSnapshot(ctx context.Context) error {
	mounts, err := mountinfo.GetMounts(nil)
	if err != nil {
		return errors.Wrap(err, "failed to get mounts")
	}
	for _, m := range mounts {
		if strings.HasPrefix(m.Mountpoint, filepath.Join(o.root, "snapshots")) {
			if err := syscall.Unmount(m.Mountpoint, syscall.MNT_FORCE); err != nil {
				return errors.Wrapf(err, "failed to unmount %s", m.Mountpoint)
			}
		}
	}

	if o.noRestore {
		return nil
	}

	var task []snapshots.Info
	if err := o.Walk(ctx, func(ctx context.Context, info snapshots.Info) error {
		if _, ok := info.Labels[remoteLabel]; ok {
			task = append(task, info)
		}
		return nil
	}); err != nil && !errdefs.IsNotFound(err) {
		return errors.Wrap(err, "failed to walk snapshots")
	}
	for _, info := range task {
		if err := o.prepareRemoteSnapshot(ctx, info.Name, info.Labels); err != nil {
			if o.allowInvalidMountsOnRestart {
				log.G(ctx).WithError(err).Warnf("failed to restore remote snapshot %s; remove this snapshot manually", info.Name)
				// This snapshot mount is invalid but allow this.
				// NOTE: snapshotter.Mount() will fail to return the mountpoint of these invalid snapshots so
				//       containerd cannot use them anymore. User needs to manually remove the snapshots from
				//       containerd's metadata store using ctr (e.g. `ctr snapshot rm`).
				continue
			}
			return errors.Wrapf(err, "failed to prepare remote snapshot %s", info.Name)
		}
	}

	return nil
}

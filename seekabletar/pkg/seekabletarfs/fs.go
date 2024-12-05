package seekabletarfs

import (
	"io"
	"os"
	"time"

	fusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/hanwen/go-fuse/v2/fuse/pathfs"
	"github.com/pkg/errors"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/seekabletar"
)

type FileSystem struct {
	pathfs.FileSystem
	stream io.ReaderAt
	index  *seekabletar.TarTOCIndex
	root   *FSNode
	prefix string
}

func NewFileSystem(stream io.ReaderAt, index *seekabletar.TarTOCIndex, prefix string) (*FileSystem, error) {
	root := index.Get("/")
	if root == nil {
		return nil, errors.New("root not found")
	}
	fs := &FileSystem{
		stream: stream,
		index:  index,
		prefix: prefix,
	}

	fs.root = &FSNode{
		filesystem: fs,
		entry:      root,
		attr:       getAttr(root),
	}

	return fs, nil
}

func (fs *FileSystem) Root() (fusefs.InodeEmbedder, error) {
	if fs.root == nil {
		return nil, errors.New("root not initialized")
	}
	return fs.root, nil
}

func Mount(tarPath, mountPoint string) error {
	file, err := os.Open(tarPath)
	if err != nil {
		return errors.Wrap(err, "failed to open tar file")
	}
	defer file.Close()

	toc, err := seekabletar.GetTOCFromSeekableTar(file)
	if err != nil {
		return errors.Wrap(err, "failed to get toc")
	}

	index := seekabletar.NewTOCIndex(toc)

	fs, err := NewFileSystem(file, index, "")
	if err != nil {
		return err
	}

	root, err := fs.Root()
	if err != nil {
		return err
	}
	attrTimeout := time.Second * 60
	entryTimeout := time.Second * 60
	fsOptions := &fusefs.Options{
		AttrTimeout:  &attrTimeout,
		EntryTimeout: &entryTimeout,
	}

	nodeFS := fusefs.NewNodeFS(root, fsOptions)

	server, err := fuse.NewServer(nodeFS, mountPoint, &fuse.MountOptions{
		MaxBackground:        512,
		DisableXAttrs:        true,
		EnableSymlinkCaching: true,
		SyncRead:             false,
		RememberInodes:       true,
		MaxReadAhead:         1 << 17,
		Name:                 "seekabletarfs",
	})
	if err != nil {
		return errors.Wrap(err, "failed to create server")
	}

	startServer := func() error { // nolint:unparam
		go server.Serve()

		if err := server.WaitMount(); err != nil {
			return errors.Wrap(err, "failed to wait mount")
		}

		server.Wait()

		return nil
	}

	return startServer()
}

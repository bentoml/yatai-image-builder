package seekabletarfs

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/logger"
	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/seekabletar"
)

const (
	blockSize         = 4096
	physicalBlockSize = 512
	// physicalBlockRatio is the ratio of blockSize to physicalBlockSize.
	// It can be used to convert from # blockSize-byte blocks to # physicalBlockSize-byte blocks
	physicalBlockRatio = blockSize / physicalBlockSize
	statFileMode       = syscall.S_IFREG | 0400 // -r--------
	stateDirMode       = syscall.S_IFDIR | 0500 // dr-x------
)

func getAttr(entry *seekabletar.TarTOCEntry) fuse.Attr {
	attr := fuse.Attr{
		Ino:  entry.Off, // nolint:gosec
		Mode: entry.FuseMode(),
		Size: entry.Size,
		Owner: fuse.Owner{
			Uid: uint32(entry.Uid), // nolint:gosec
			Gid: uint32(entry.Gid), // nolint:gosec
		},
	}

	if entry.Mode&os.ModeSymlink != 0 {
		attr.Size = uint64(len(entry.Target))
	}

	attr.Blksize = blockSize
	attr.Blocks = (attr.Size + uint64(attr.Blksize) - 1) / uint64(attr.Blksize) * physicalBlockRatio
	attr.SetTimes(&entry.ATime, &entry.MTime, &entry.CTime)
	attr.Nlink = entry.Nlink

	return attr
}

type FSNode struct {
	fs.Inode
	filesystem *FileSystem
	entry      *seekabletar.TarTOCEntry
	attr       fuse.Attr
}

func (n *FSNode) log(format string, v ...interface{}) {
	logger.L().Debug(format, v...)
}

func (n *FSNode) OnAdd(ctx context.Context) {
	n.log("OnAdd called")
}

func (n *FSNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.log("Getattr called")

	attr := getAttr(n.entry)

	// Fill in the AttrOut struct
	out.Attr = attr

	return fs.OK
}

func (n *FSNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.log("Lookup called", slog.String("name", name))

	// Create the full path of the child node
	childPath := filepath.Join(n.entry.Path, name)

	// Lookup the child node
	child := n.filesystem.index.Get(childPath)
	if child == nil {
		// No child with the requested name exists
		return nil, syscall.ENOENT
	}

	// Fill out the child node's attributes
	out.Attr = getAttr(child)

	childInode := n.NewInode(ctx, &FSNode{filesystem: n.filesystem, entry: child, attr: out.Attr}, fs.StableAttr{Mode: out.Attr.Mode, Ino: out.Attr.Ino})

	return childInode, fs.OK
}

func (n *FSNode) Opendir(ctx context.Context) syscall.Errno {
	n.log("Opendir called")
	return 0
}

func (n *FSNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	n.log("Open called with flags: %v", flags)
	return nil, 0, fs.OK
}

func (n *FSNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n.log("Read called with offset: %v", off)

	// Don't even try to read 0 byte files
	if n.entry.Size == 0 {
		nRead := 0
		return fuse.ReadResultData(dest[:nRead]), fs.OK
	}

	nRead, err := n.filesystem.stream.ReadAt(dest, int64(n.entry.Off)+off) // nolint:gosec
	if err != nil {
		return nil, syscall.EIO
	}

	return fuse.ReadResultData(dest[:nRead]), fs.OK
}

func (n *FSNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	n.log("Readlink called", slog.String("path", n.entry.Path), slog.String("target", n.entry.Target))

	if n.entry.Target == "" {
		// This node is not a symlink
		return nil, syscall.EINVAL
	}

	// Use the symlink target path directly
	symlinkTarget := n.entry.Target

	// In this case, we don't need to read the file
	return []byte(symlinkTarget), fs.OK
}

func (n *FSNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	n.log("Readdir called")

	dirEntries := n.filesystem.index.ListDirectory(n.entry.Path)
	return fs.NewListDirStream(dirEntries), fs.OK
}

func (n *FSNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (inode *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	n.log("Create called with name: %s, flags: %v, mode: %v", name, flags, mode)
	return nil, nil, 0, syscall.EROFS
}

func (n *FSNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.log("Mkdir called with name: %s, mode: %v", name, mode)
	return nil, syscall.EROFS
}

func (n *FSNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	n.log("Rmdir called with name: %s", name)
	return syscall.EROFS
}

func (n *FSNode) Unlink(ctx context.Context, name string) syscall.Errno {
	n.log("Unlink called with name: %s", name)
	return syscall.EROFS
}

func (n *FSNode) Rename(ctx context.Context, oldName string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	n.log("Rename called with oldName: %s, newName: %s, flags: %v", oldName, newName, flags)
	return syscall.EROFS
}

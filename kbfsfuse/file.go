package main

import (
	"log"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"github.com/keybase/kbfs/libkbfs"
	"golang.org/x/net/context"
)

// File represents KBFS files.
type File struct {
	fs.NodeRef

	parent   *Dir
	pathNode libkbfs.PathNode
}

var _ fs.Node = (*File)(nil)

// Attr implements the fs.Node interface for File.
func (f *File) Attr(ctx context.Context, a *fuse.Attr) error {
	f.parent.folder.mu.RLock()
	defer f.parent.folder.mu.RUnlock()

	p := f.getPathLocked()
	de, err := statPath(ctx, f.parent.folder.fs.config.KBFSOps(), p)
	if err != nil {
		return err
	}

	fillAttr(de, a)
	a.Mode = 0644
	if de.Type == libkbfs.Exec {
		a.Mode |= 0111
	}
	return nil
}

func (f *File) getPathLocked() libkbfs.Path {
	p := f.parent.getPathLocked()
	p.Path = append(p.Path, f.pathNode)
	return p
}

// Update the PathNode stored here, and in parents.
//
// Caller is responsible for locking.
func (f *File) updatePathLocked(p libkbfs.Path) {
	pNode := p.Path[len(p.Path)-1]
	if f.pathNode.Name != pNode.Name {
		return
	}
	f.pathNode = pNode
	p.Path = p.Path[:len(p.Path)-1]
	f.parent.updatePathLocked(p)
}

var _ fs.NodeFsyncer = (*File)(nil)

func (f *File) sync(ctx context.Context) error {
	f.parent.folder.mu.Lock()
	defer f.parent.folder.mu.Unlock()

	p, err := f.parent.folder.fs.config.KBFSOps().Sync(ctx, f.getPathLocked())
	if err != nil {
		return err
	}
	f.updatePathLocked(p)

	return nil
}

// Fsync implements the fs.NodeFsyncer interface for File.
func (f *File) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	return f.sync(ctx)
}

var _ fs.Handle = (*File)(nil)

var _ fs.HandleReader = (*File)(nil)

// Read implements the fs.HandleReader interface for File.
func (f *File) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	f.parent.folder.mu.RLock()
	defer f.parent.folder.mu.RUnlock()

	p := f.getPathLocked()
	n, err := f.parent.folder.fs.config.KBFSOps().Read(
		ctx, p, resp.Data[:cap(resp.Data)], req.Offset)
	resp.Data = resp.Data[:n]
	return err
}

var _ fs.HandleWriter = (*File)(nil)

// Write implements the fs.HandleWriter interface for File.
func (f *File) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	f.parent.folder.mu.Lock()
	defer f.parent.folder.mu.Unlock()

	p := f.getPathLocked()
	if err := f.parent.folder.fs.config.KBFSOps().Write(
		ctx, p, req.Data, req.Offset); err != nil {
		return err
	}
	resp.Size = len(req.Data)
	return nil
}

var _ fs.HandleFlusher = (*File)(nil)

// Flush implements the fs.HandleFlusher interface for File.
func (f *File) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	// I'm not sure about the guarantees from KBFSOps, so we don't
	// differentiate between Flush and Fsync.
	return f.sync(ctx)
}

var _ fs.NodeSetattrer = (*File)(nil)

// Setattr implements the fs.NodeSetattrer interface for File.
func (f *File) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	f.parent.folder.mu.Lock()
	defer f.parent.folder.mu.Unlock()

	valid := req.Valid
	if valid.Size() {
		if err := f.parent.folder.fs.config.KBFSOps().Truncate(
			ctx, f.getPathLocked(), req.Size); err != nil {
			return err
		}
		valid &^= fuse.SetattrSize
	}

	if valid.Mode() {
		// Unix has 3 exec bits, KBFS has one; we follow the user-exec bit.
		exec := req.Mode&0100 != 0
		p, err := f.parent.folder.fs.config.KBFSOps().SetEx(
			ctx, f.getPathLocked(), exec)
		if err != nil {
			return err
		}
		f.updatePathLocked(p)
		valid &^= fuse.SetattrMode
	}

	if valid.Mtime() {
		p, err := f.parent.folder.fs.config.KBFSOps().SetMtime(
			ctx, f.getPathLocked(), &req.Mtime)
		if err != nil {
			return err
		}
		f.updatePathLocked(p)
		valid &^= fuse.SetattrMtime
	}

	// KBFS has no concept of persistent atime; explicitly don't handle it
	valid &^= fuse.SetattrAtime

	// things we don't need to explicitly handle
	valid &^= fuse.SetattrLockOwner | fuse.SetattrHandle

	if valid != 0 {
		// don't let an unhandled operation slip by without error
		log.Printf("Setattr did not handle %v", valid)
		return fuse.ENOSYS
	}
	return nil
}

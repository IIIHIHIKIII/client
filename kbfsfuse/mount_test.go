package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"bazil.org/fuse/fs/fstestutil"

	"github.com/keybase/kbfs/libkbfs"
	"golang.org/x/net/context"
)

func makeFS(t testing.TB, config *libkbfs.ConfigLocal) *fstestutil.Mount {
	filesys := &FS{
		ctx:    context.Background(),
		config: config,
	}
	mnt, err := fstestutil.MountedT(t, filesys)
	if err != nil {
		t.Fatal(err)
	}
	return mnt
}

type fileInfoCheck func(fi os.FileInfo) error

func mustBeDir(fi os.FileInfo) error {
	if !fi.IsDir() {
		return fmt.Errorf("not a directory: %v", fi)
	}
	return nil
}

func checkDir(t testing.TB, dir string, want map[string]fileInfoCheck) {
	// make a copy of want, to be safe
	{
		tmp := make(map[string]fileInfoCheck, len(want))
		for k, v := range want {
			tmp[k] = v
		}
		want = tmp
	}

	fis, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, fi := range fis {
		if check, ok := want[fi.Name()]; ok {
			delete(want, fi.Name())
			if check != nil {
				if err := check(fi); err != nil {
					t.Errorf("check failed: %v: %v", fi.Name(), err)
				}
			}
			continue
		}
		t.Errorf("unexpected direntry: %q size=%v mode=%v", fi.Name(), fi.Size(), fi.Mode())
	}
	for filename := range want {
		t.Errorf("never saw file: %v", filename)
	}
}

// fsTimeEqual compares two filesystem-related timestamps.
//
// On platforms that don't use nanosecond-accurate timestamps in their
// filesystem APIs, it truncates the timestamps to make them
// comparable.
func fsTimeEqual(a, b time.Time) bool {
	if runtime.GOOS == "darwin" {
		a = a.Truncate(1 * time.Second)
		b = b.Truncate(1 * time.Second)
	}
	return a == b
}

func TestStatRoot(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	fi, err := os.Lstat(mnt.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Mode().String(), `drwxr-xr-x`; g != e {
		t.Errorf("wrong mode for folder: %q != %q", g, e)
	}
}

func TestStatMyFolder(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	fi, err := os.Lstat(path.Join(mnt.Dir, "jdoe"))
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Mode().String(), `drwx------`; g != e {
		t.Errorf("wrong mode for folder: %q != %q", g, e)
	}
}

func TestStatAlias(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe,jdoe")
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Mode().String(), `Lrwxrwxrwx`; g != e {
		t.Errorf("wrong mode for alias : %q != %q", g, e)
	}
	target, err := os.Readlink(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := target, "jdoe"; g != e {
		t.Errorf("wrong alias symlink target: %q != %q", g, e)
	}
}

func TestStatMyPublic(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	fi, err := os.Lstat(path.Join(mnt.Dir, "jdoe", "public"))
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Mode().String(), `drwxr-xr-x`; g != e {
		t.Errorf("wrong mode for folder: %q != %q", g, e)
	}
}

func TestReaddirRoot(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	{
		// Force FakeMDServer to have some DirIds it can present to us
		// as favorites. Don't go through VFS to avoid caching causing
		// false positives.
		dh, err := libkbfs.ParseDirHandle(context.Background(), config, "jdoe")
		if err != nil {
			t.Fatalf("cannot parse jdoe as folder: %v", err)
		}
		if _, _, err := config.KBFSOps().GetOrCreateRootPathForHandle(
			context.Background(), dh); err != nil {
			t.Fatalf("cannot set up a favorite: %v", err)
		}
	}

	checkDir(t, mnt.Dir, map[string]fileInfoCheck{
		"jdoe": mustBeDir,
	})
}

func TestReaddirMyFolderEmpty(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": mustBeDir,
	})
}

func TestReaddirMyFolderWithFiles(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	files := map[string]fileInfoCheck{
		"public": mustBeDir,
		"one":    nil,
		"two":    nil,
	}
	for filename, check := range files {
		if check != nil {
			// only set up the files
			continue
		}
		if err := ioutil.WriteFile(path.Join(mnt.Dir, "jdoe", filename), []byte("data for "+filename), 0644); err != nil {
			t.Fatal(err)
		}
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), files)
}

func TestCreateThenRead(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const input = "hello, world\n"
	if _, err := io.WriteString(f, input); err != nil {
		t.Fatalf("write error: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("error on close: %v", err)
	}

	buf, err := ioutil.ReadFile(p)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if g, e := string(buf), input; g != e {
		t.Errorf("bad file contents: %q != %q", g, e)
	}
}

func TestReadUnflushed(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const input = "hello, world\n"
	if _, err := io.WriteString(f, input); err != nil {
		t.Fatalf("write error: %v", err)
	}
	// explicitly no close here

	buf, err := ioutil.ReadFile(p)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if g, e := string(buf), input; g != e {
		t.Errorf("bad file contents: %q != %q", g, e)
	}
}

func TestMountAgain(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")

	const input = "hello, world\n"
	const filename = "myfile"
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		p := path.Join(mnt.Dir, "jdoe", filename)
		if err := ioutil.WriteFile(p, []byte(input), 0644); err != nil {
			t.Fatal(err)
		}
	}()

	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()
		p := path.Join(mnt.Dir, "jdoe", filename)
		buf, err := ioutil.ReadFile(p)
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
		if g, e := string(buf), input; g != e {
			t.Errorf("bad file contents: %q != %q", g, e)
		}
	}()
}

func TestCreateExecutable(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	if err := ioutil.WriteFile(p, []byte("fake binary"), 0755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Mode().String(), `-rwxr-xr-x`; g != e {
		t.Errorf("wrong mode for executable: %q != %q", g, e)
	}
}

func TestMkdir(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "mydir")
	if err := os.Mkdir(p, 0755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Mode().String(), `drwx------`; g != e {
		t.Errorf("wrong mode for subdir: %q != %q", g, e)
	}
}

func TestMkdirAndCreateDeep(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	const input = "hello, world\n"

	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		one := path.Join(mnt.Dir, "jdoe", "one")
		if err := os.Mkdir(one, 0755); err != nil {
			t.Fatal(err)
		}
		two := path.Join(one, "two")
		if err := os.Mkdir(two, 0755); err != nil {
			t.Fatal(err)
		}
		three := path.Join(two, "three")
		if err := ioutil.WriteFile(three, []byte(input), 0644); err != nil {
			t.Fatal(err)
		}
	}()

	// unmount to flush cache
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		p := path.Join(mnt.Dir, "jdoe", "one", "two", "three")
		buf, err := ioutil.ReadFile(p)
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
		if g, e := string(buf), input; g != e {
			t.Errorf("bad file contents: %q != %q", g, e)
		}
	}()
}

func TestSymlink(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")

	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		p := path.Join(mnt.Dir, "jdoe", "mylink")
		if err := os.Symlink("myfile", p); err != nil {
			t.Fatal(err)
		}
	}()

	// unmount to flush cache
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		p := path.Join(mnt.Dir, "jdoe", "mylink")
		target, err := os.Readlink(p)
		if err != nil {
			t.Fatal(err)
		}
		if g, e := target, "myfile"; g != e {
			t.Errorf("bad symlink target: %q != %q", g, e)
		}
	}()
}

func TestRename(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p1 := path.Join(mnt.Dir, "jdoe", "old")
	p2 := path.Join(mnt.Dir, "jdoe", "new")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p1, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(p1, p2); err != nil {
		t.Fatal(err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
		"new": func(fi os.FileInfo) error {
			if fi.Size() != int64(len(input)) {
				return fmt.Errorf("Bad file size: %d", fi.Size())
			}
			return nil
		},
	})

	buf, err := ioutil.ReadFile(p2)
	if err != nil {
		t.Errorf("read error: %v", err)
	}
	if g, e := string(buf), input; g != e {
		t.Errorf("bad file contents: %q != %q", g, e)
	}

	if _, err := ioutil.ReadFile(p1); !os.IsNotExist(err) {
		t.Errorf("old name still exists: %v", err)
	}
}

func TestRenameOverwrite(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p1 := path.Join(mnt.Dir, "jdoe", "old")
	p2 := path.Join(mnt.Dir, "jdoe", "new")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p1, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(p2, []byte("loser\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(p1, p2); err != nil {
		t.Fatal(err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
		"new":    nil,
	})

	buf, err := ioutil.ReadFile(p2)
	if err != nil {
		t.Errorf("read error: %v", err)
	}
	if g, e := string(buf), input; g != e {
		t.Errorf("bad file contents: %q != %q", g, e)
	}

	if _, err := ioutil.ReadFile(p1); !os.IsNotExist(err) {
		t.Errorf("old name still exists: %v", err)
	}
}

func TestRenameCrossDir(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	if err := os.Mkdir(path.Join(mnt.Dir, "jdoe", "one"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path.Join(mnt.Dir, "jdoe", "two"), 0755); err != nil {
		t.Fatal(err)
	}
	p1 := path.Join(mnt.Dir, "jdoe", "one", "old")
	p2 := path.Join(mnt.Dir, "jdoe", "two", "new")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p1, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(p1, p2); err != nil {
		t.Fatal(err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe", "one"), map[string]fileInfoCheck{})
	checkDir(t, path.Join(mnt.Dir, "jdoe", "two"), map[string]fileInfoCheck{
		"new": nil,
	})

	buf, err := ioutil.ReadFile(p2)
	if err != nil {
		t.Errorf("read error: %v", err)
	}
	if g, e := string(buf), input; g != e {
		t.Errorf("bad file contents: %q != %q", g, e)
	}

	if _, err := ioutil.ReadFile(p1); !os.IsNotExist(err) {
		t.Errorf("old name still exists: %v", err)
	}
}

func TestRenameCrossFolder(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe", "wsmith")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p1 := path.Join(mnt.Dir, "jdoe", "old")
	p2 := path.Join(mnt.Dir, "wsmith,jdoe", "new")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p1, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	err := os.Rename(p1, p2)
	if err == nil {
		t.Fatalf("expected an error from rename: %v", err)
	}
	lerr, ok := err.(*os.LinkError)
	if !ok {
		t.Fatalf("expected a LinkError from rename: %v", err)
	}
	if g, e := lerr.Op, "rename"; g != e {
		t.Errorf("wrong LinkError.Op: %q != %q", g, e)
	}
	if g, e := lerr.Old, p1; g != e {
		t.Errorf("wrong LinkError.Old: %q != %q", g, e)
	}
	if g, e := lerr.New, p2; g != e {
		t.Errorf("wrong LinkError.New: %q != %q", g, e)
	}
	if g, e := lerr.Err, syscall.EXDEV; g != e {
		t.Errorf("expected EXDEV: %T %v", lerr.Err, lerr.Err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
		"old":    nil,
	})
	checkDir(t, path.Join(mnt.Dir, "wsmith,jdoe"), map[string]fileInfoCheck{
		"public": nil,
	})

	buf, err := ioutil.ReadFile(p1)
	if err != nil {
		t.Errorf("read error: %v", err)
	}
	if g, e := string(buf), input; g != e {
		t.Errorf("bad file contents: %q != %q", g, e)
	}

	if _, err := ioutil.ReadFile(p2); !os.IsNotExist(err) {
		t.Errorf("new name exists even on error: %v", err)
	}
}

func TestWriteThenRenameDesired(t *testing.T) {
	t.Skip("Broken; second write fails with I/O error")
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p1 := path.Join(mnt.Dir, "jdoe", "old")
	p2 := path.Join(mnt.Dir, "jdoe", "new")

	f, err := os.Create(p1)
	if err != nil {
		t.Fatalf("cannot create file: %v", err)
	}
	defer f.Close()

	// write to the file
	const input = "hello, world\n"
	if _, err := f.Write([]byte(input)); err != nil {
		t.Fatalf("cannot write: %v", err)
	}

	// now rename the file while it's still open
	if err := os.Rename(p1, p2); err != nil {
		t.Fatal(err)
	}

	// check that the new path has the right length still
	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
		"new": func(fi os.FileInfo) error {
			if fi.Size() != int64(len(input)) {
				return fmt.Errorf("Bad file size: %d", fi.Size())
			}
			return nil
		},
	})

	// write again to the same file
	const input2 = "goodbye, world\n"
	if _, err := f.Write([]byte(input2)); err != nil {
		t.Fatalf("cannot write after rename: %v", err)
	}

	buf, err := ioutil.ReadFile(p2)
	if err != nil {
		t.Errorf("read error: %v", err)
	}
	if g, e := string(buf), input+input2; g != e {
		t.Errorf("bad file contents: %q != %q", g, e)
	}

	if _, err := ioutil.ReadFile(p1); !os.IsNotExist(err) {
		t.Errorf("old name still exists: %v", err)
	}
}

func TestRemoveFile(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
	})

	if _, err := ioutil.ReadFile(p); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestRemoveDir(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "mydir")
	if err := os.Mkdir(p, 0755); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Rmdir(p); err != nil {
		t.Fatal(err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
	})

	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestRemoveDirNotEmpty(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "mydir")
	if err := os.Mkdir(p, 0755); err != nil {
		t.Fatal(err)
	}
	pFile := path.Join(p, "myfile")
	if err := ioutil.WriteFile(pFile, []byte("i'm important"), 0644); err != nil {
		t.Fatal(err)
	}

	err := syscall.Rmdir(p)
	if g, e := err, syscall.ENOTEMPTY; g != e {
		t.Fatalf("wrong error from rmdir: %v (%T) != %v (%T)", g, g, e, e)
	}

	if _, err := ioutil.ReadFile(pFile); err != nil {
		t.Errorf("file was lost: %v", err)
	}
}

func TestRemoveFileWhileOpenWriting_Desired(t *testing.T) {
	// when this works, rename function and remove
	// TestRemoveFileWhileOpenWriting_Current
	t.Skip("Not implemented yet. https://github.com/keybase/kbfs/issues/81")
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("cannot create file: %v", err)
	}
	defer f.Close()

	if err := os.Remove(p); err != nil {
		t.Fatalf("cannot delete file: %v", err)
	}

	// this must not resurrect a deleted file
	const input = "hello, world\n"
	if _, err := f.Write([]byte(input)); err != nil {
		t.Fatalf("cannot write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("error on close: %v", err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{})

	if _, err := ioutil.ReadFile(p); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestRemoveFileWhileOpenWriting_Current(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("cannot create file: %v", err)
	}
	defer f.Close()

	if err := os.Remove(p); err != nil {
		t.Fatalf("cannot delete file: %v", err)
	}

	// this must not resurrect a deleted file
	const input = "hello, world\n"
	_, err = f.Write([]byte(input))
	if err == nil {
		t.Fatalf("expected an error from write")
	}
	perr, ok := err.(*os.PathError)
	if !ok {
		t.Fatalf("expected a PathError from write: %v", err)
	}
	if g, e := perr.Op, "write"; g != e {
		t.Errorf("wrong PathError.Op: %q != %q", g, e)
	}
	if g, e := perr.Path, p; g != e {
		t.Errorf("wrong PathError.Path: %q != %q", g, e)
	}
	// TODO want ESTALE or ENOENT, maybe?
	if g, e := perr.Err, syscall.EIO; g != e {
		t.Errorf("expected EIO: %T %v", perr.Err, perr.Err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("error on close: %v", err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
	})

	if _, err := ioutil.ReadFile(p); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestRemoveFileWhileOpenReading_Desired(t *testing.T) {
	// when this works, rename function and remove
	// TestRemoveFileWhileOpenWriting_Current
	t.Skip("Not implemented yet. https://github.com/keybase/kbfs/issues/82")
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("cannot open file: %v", err)
	}
	defer f.Close()

	if err := os.Remove(p); err != nil {
		t.Fatalf("cannot delete file: %v", err)
	}

	buf, err := ioutil.ReadAll(f)
	if err != nil {
		t.Fatalf("cannot read unlinked file: %v", err)
	}
	if g, e := string(buf), input; g != e {
		t.Errorf("read wrong content: %q != %q", g, e)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("error on close: %v", err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
	})

	if _, err := ioutil.ReadFile(p); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestRemoveFileWhileOpenReading_Current(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("on OS X, the relevant reads seems to be served from page cache, so we can't get this to trigger")
	}
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("cannot open file: %v", err)
	}
	defer f.Close()

	if err := os.Remove(p); err != nil {
		t.Fatalf("cannot delete file: %v", err)
	}

	_, err = ioutil.ReadAll(f)
	if err == nil {
		t.Fatalf("expected an error from read")
	}
	perr, ok := err.(*os.PathError)
	if !ok {
		t.Fatalf("expected a PathError from read: %v", err)
	}
	if g, e := perr.Op, "read"; g != e {
		t.Errorf("wrong PathError.Op: %q != %q", g, e)
	}
	if g, e := perr.Path, p; g != e {
		t.Errorf("wrong PathError.Path: %q != %q", g, e)
	}
	if g, e := perr.Err, syscall.ESTALE; g != e {
		t.Errorf("expected ESTALE: %T %v", perr.Err, perr.Err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("error on close: %v", err)
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": nil,
	})

	if _, err := ioutil.ReadFile(p); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestTruncateGrow(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	const newSize = 100
	if err := os.Truncate(p, newSize); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Size(), int64(newSize); g != e {
		t.Errorf("wrong size: %v != %v", g, e)
	}

	buf, err := ioutil.ReadFile(p)
	if err != nil {
		t.Fatalf("cannot read unlinked file: %v", err)
	}
	if g, e := string(buf), input+strings.Repeat("\x00", newSize-len(input)); g != e {
		t.Errorf("read wrong content: %q != %q", g, e)
	}
}

func TestTruncateShrink(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	const newSize = 4
	if err := os.Truncate(p, newSize); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Size(), int64(newSize); g != e {
		t.Errorf("wrong size: %v != %v", g, e)
	}

	buf, err := ioutil.ReadFile(p)
	if err != nil {
		t.Fatalf("cannot read unlinked file: %v", err)
	}
	if g, e := string(buf), input[:newSize]; g != e {
		t.Errorf("read wrong content: %q != %q", g, e)
	}
}

func TestChmodExec(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(p, 0744); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Mode().String(), `-rwxr-xr-x`; g != e {
		t.Errorf("wrong mode: %q != %q", g, e)
	}
}

func TestChmodNonExec(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p, []byte(input), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(p, 0655); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.Mode().String(), `-rw-r--r--`; g != e {
		t.Errorf("wrong mode: %q != %q", g, e)
	}
}

func TestSetattrMtime(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	const input = "hello, world\n"
	if err := ioutil.WriteFile(p, []byte(input), 0755); err != nil {
		t.Fatal(err)
	}

	mtime := time.Date(2015, 1, 2, 3, 4, 5, 6, time.Local)
	// KBFS does not respect atime (which is ok), but we need to give
	// something to the syscall.
	atime := time.Date(2015, 7, 8, 9, 10, 11, 12, time.Local)
	if err := os.Chtimes(p, atime, mtime); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if g, e := fi.ModTime(), mtime; !fsTimeEqual(g, e) {
		t.Errorf("wrong mtime: %v !~= %v", g, e)
	}
}

func TestFsync(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	p := path.Join(mnt.Dir, "jdoe", "myfile")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const input = "hello, world\n"
	if _, err := io.WriteString(f, input); err != nil {
		t.Fatalf("write error: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("fsync error: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}
}

func TestReaddirMyPublic(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe")
	mnt := makeFS(t, config)
	defer mnt.Close()

	files := map[string]fileInfoCheck{
		"one": nil,
		"two": nil,
	}
	for filename := range files {
		if err := ioutil.WriteFile(path.Join(mnt.Dir, "jdoe", "public", filename), []byte("data for "+filename), 0644); err != nil {
			t.Fatal(err)
		}
	}

	checkDir(t, path.Join(mnt.Dir, "jdoe", "public"), files)
}

func TestReaddirOtherFolderAsReader(t *testing.T) {
	// TODO bystander is a bug workaround
	// https://github.com/keybase/kbfs/issues/91
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe", "wsmith", "bystander")
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		// cause the folder to exist
		if err := ioutil.WriteFile(path.Join(mnt.Dir, "bystander,jdoe#wsmith", "myfile"), []byte("data for myfile"), 0644); err != nil {
			t.Fatal(err)
		}
	}()

	c2 := libkbfs.ConfigAsUser(config, "wsmith")
	mnt := makeFS(t, c2)
	defer mnt.Close()

	checkDir(t, path.Join(mnt.Dir, "bystander,jdoe#wsmith"), map[string]fileInfoCheck{
		"myfile": nil,
	})
}

func TestStatOtherFolder(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe", "wsmith")
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		// cause the folder to exist
		if err := ioutil.WriteFile(path.Join(mnt.Dir, "jdoe", "myfile"), []byte("data for myfile"), 0644); err != nil {
			t.Fatal(err)
		}
	}()

	c2 := libkbfs.ConfigAsUser(config, "wsmith")
	mnt := makeFS(t, c2)
	defer mnt.Close()

	fi, err := os.Lstat(path.Join(mnt.Dir, "jdoe"))
	if err != nil {
		t.Fatal(err)
	}
	// TODO figure out right modes, note owner is the person running
	// fuse, not the person owning the folder
	if g, e := fi.Mode().String(), `drwx------`; g != e {
		t.Errorf("wrong mode for folder: %q != %q", g, e)
	}
}

func TestStatOtherFolderPublic(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe", "wsmith")
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		// cause the folder to exist
		if err := ioutil.WriteFile(path.Join(mnt.Dir, "jdoe", "public", "myfile"), []byte("data for myfile"), 0644); err != nil {
			t.Fatal(err)
		}
	}()

	c2 := libkbfs.ConfigAsUser(config, "wsmith")
	mnt := makeFS(t, c2)
	defer mnt.Close()

	fi, err := os.Lstat(path.Join(mnt.Dir, "jdoe", "public"))
	if err != nil {
		t.Fatal(err)
	}
	// TODO figure out right modes, note owner is the person running
	// fuse, not the person owning the folder
	if g, e := fi.Mode().String(), `drwxr-xr-x`; g != e {
		t.Errorf("wrong mode for folder: %q != %q", g, e)
	}
}

func TestReadPublicFile(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe", "wsmith")
	const input = "hello, world\n"
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		// cause the folder to exist
		if err := ioutil.WriteFile(path.Join(mnt.Dir, "jdoe", "public", "myfile"), []byte(input), 0644); err != nil {
			t.Fatal(err)
		}
	}()

	c2 := libkbfs.ConfigAsUser(config, "wsmith")
	mnt := makeFS(t, c2)
	defer mnt.Close()

	buf, err := ioutil.ReadFile(path.Join(mnt.Dir, "jdoe", "public", "myfile"))
	if err != nil {
		t.Fatal(err)
	}
	if g, e := string(buf), input; g != e {
		t.Errorf("bad file contents: %q != %q", g, e)
	}
}

func TestReaddirOtherFolderPublicAsAnyone(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe", "wsmith")
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		// cause the folder to exist
		if err := ioutil.WriteFile(path.Join(mnt.Dir, "jdoe", "public", "myfile"), []byte("data for myfile"), 0644); err != nil {
			t.Fatal(err)
		}
	}()

	c2 := libkbfs.ConfigAsUser(config, "wsmith")
	mnt := makeFS(t, c2)
	defer mnt.Close()

	checkDir(t, path.Join(mnt.Dir, "jdoe", "public"), map[string]fileInfoCheck{
		"myfile": nil,
	})
}

func TestReaddirOtherFolderAsAnyone(t *testing.T) {
	config := libkbfs.MakeTestConfigOrBust(t, *BServerRemote, "jdoe", "wsmith")
	func() {
		mnt := makeFS(t, config)
		defer mnt.Close()

		// cause the folder to exist
		if err := ioutil.WriteFile(path.Join(mnt.Dir, "jdoe", "public", "myfile"), []byte("data for myfile"), 0644); err != nil {
			t.Fatal(err)
		}
	}()

	c2 := libkbfs.ConfigAsUser(config, "wsmith")
	mnt := makeFS(t, c2)
	defer mnt.Close()

	checkDir(t, path.Join(mnt.Dir, "jdoe"), map[string]fileInfoCheck{
		"public": mustBeDir,
	})
}

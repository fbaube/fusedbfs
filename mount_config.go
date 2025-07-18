// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fuse

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"
)

// Optional configuration accepted by Mount.
type MountConfig struct {
	// The context from which every op read from the connetion by the sever
	// should inherit. If nil, context.Background() will be used.
	OpContext context.Context

	// If non-empty, the name of the file system as displayed by e.g. `mount`.
	// This is important because the `umount` command requires root privileges if
	// it doesn't agree with /etc/fstab.
	FSName string

	// Mount the file system in read-only mode. File modes will appear as normal,
	// but opening a file for writing and metadata operations like chmod,
	// chtimes, etc. will fail.
	ReadOnly bool

	// A logger to use for logging errors. All errors are logged, with the
	// exception of a few blacklisted errors that are expected. If nil, no error
	// logging is performed.
	ErrorLogger *log.Logger

	// A logger to use for logging debug information. If nil, no debug logging is
	// performed.
	DebugLogger *log.Logger

	// Linux only. OS X always behaves as if writeback caching is disabled.
	//
	// By default on Linux we allow the kernel to perform writeback caching
	// (https://tinyurl.com/3ma8ypeu):
	//
	// *   When the user calls write(2), the kernel sticks the user's data into
	//     its page cache. Only later does it call through to the file system,
	//     potentially after coalescing multiple small user writes.
	//
	// *   The file system may receive multiple write ops from the kernel
	//     concurrently if there is a lot of page cache data to flush.
	//
	// *   Write performance may be significantly improved due to the user and
	//     the kernel not waiting for serial round trips to the file system. This
	//     is especially true if the user makes tiny writes.
	//
	// *   close(2) (and anything else calling f_op->flush) causes all dirty
	//     pages to be written out before it proceeds to send a FlushFileOp
	//     (https://tinyurl.com/3ur6vmsv).
	//
	// *   Similarly, close(2) causes the kernel to send a setattr request
	//     filling in the mtime if any dirty pages were flushed, since the time
	//     at which the pages were written to the file system can't be trusted.
	//
	// *   close(2) (and anything else calling f_op->flush) writes out all dirty
	//     pages, then sends a setattr request with an appropriate mtime for
	//     those writes if there were any, and only then proceeds to send a
	//     flush.
	//
	//     Code walk:
	//
	//     *   (https://tinyurl.com/3ur6vmsv) fuse_flush calls write_inode_now
	//         before calling the file system. The latter eventually calls into
	//         __writeback_single_inode.
	//
	//     *   (https://tinyurl.com/35vtmtsz) __writeback_single_inode calls
	//         do_writepages, which writes out any dirty pages.
	//
	//     *   (https://tinyurl.com/3wv4paaf) __writeback_single_inode later
	//         calls write_inode, which calls into the superblock op struct's
	//         write_inode member. For fuse, this is fuse_write_inode
	//         (https://tinyurl.com/mrxupe98).
	//
	//     *   (https://tinyurl.com/mrxt9bta) fuse_write_inode calls
	//         fuse_flush_times.
	//
	//     *   (https://tinyurl.com/mr49cjdf) fuse_flush_times sends a setttr
	//         request for setting the inode's mtime.
	//
	// However, this brings along some caveats:
	//
	// *   The file system must handle SetInodeAttributesOp or close(2) will fail,
	//     due to the call chain into fuse_flush_times listed above.
	//
	// *   The kernel caches mtime and ctime regardless of whether the file
	//     system tells it to do so, disregarding the result of further getattr
	//     requests (https://tinyurl.com/mrxnfatv, https://tinyurl.com/27jju8n4).
	//     It appears this may be true of the file size, too. Writeback caching
	//     may therefore not be suitable for file systems where these attributes
	//     can spontaneously change for reasons the kernel doesn't observe. See
	//     https://tinyurl.com/yyprvjvs for more discussion.
	//
	// Setting DisableWritebackCaching disables this behavior. Instead the file
	// system is called one or more times for each write(2), and the user's
	// syscall doesn't return until the file system returns.
	DisableWritebackCaching bool

	// OS X only.
	//
	// Normally on OS X we mount with the novncache option
	// (https://tinyurl.com/52hz9vya), which disables entry caching in the
	// kernel. This is because macFUSE (osxfuse) does not honor the entry
	// expiration values we return to it, instead caching potentially forever
	// (https://tinyurl.com/2rr6cd3m), and it is probably better to fail to cache
	// than to cache for too long, since the latter is more likely to hide
	// consistency bugs that are difficult to detect and diagnose.
	//
	// This field disables the use of novncache, restoring entry caching. Beware:
	// the value of ChildInodeEntry.EntryExpiration is ignored by the kernel, and
	// entries will be cached for an arbitrarily long time.
	EnableVnodeCaching bool

	// Linux only.
	//
	// Linux 4.20 introduced caching symlink targets in the page cache:
	// https://github.com/torvalds/linux/commit/5571f1e65486be025f73fa6aa30fb03725d362a2
	//
	// This is not enabled by default because the old behavior masked a bug:
	// file systems could return any size in the inode attributes of
	// symlinks. After enabling caching, the specified size caps the symlink
	// target.
	EnableSymlinkCaching bool

	// Linux only.
	//
	// Tell the kernel to treat returning -ENOSYS on OpenFile as not needing
	// OpenFile calls at all (Linux >= 3.16):
	EnableNoOpenSupport bool

	// Linux only.
	//
	// Tell the kernel to treat returning -ENOSYS on OpenDir as not needing
	// OpenDir calls at all (Linux >= 5.1):
	EnableNoOpendirSupport bool

	// Disable FUSE default permissions.
	// This is useful for situations where the backing data store (e.g., S3) doesn't
	// actually utilise any form of qualifiable UNIX permissions.
	DisableDefaultPermissions bool

	// Use vectored reads.
	// Vectored read allows file systems to avoid memory copying overhead if
	// the data is already in memory when they return it to FUSE.
	// When turned on, ReadFileOp.Dst is always nil and the FS must return data
	// being read from the file as a list of slices in ReadFileOp.Data.
	UseVectoredRead bool

	// OS X only.
	//
	// The name of the mounted volume, as displayed in the Finder. If empty, a
	// default name involving the string 'osxfuse' (the old name of macFUSE)
	// is used.
	VolumeName string

	// OS X only.
	//
	// The FUSE implementation to use. One of FUSEImplFuseT (default) or
	// FUSEImplMacFUSE.
	FuseImpl FUSEImpl

	// Additional key=value options to pass unadulterated to the underlying mount
	// command. See `man 8 mount`, the fuse documentation, etc. for
	// system-specific information.
	//
	// For expert use only! May invalidate other guarantees made in the
	// documentation for this package.
	Options map[string]string

	// Sets the filesystem type (third field in /etc/mtab). /etc/mtab and
	// /proc/mounts will show the filesystem type as fuse.<Subtype>.
	// If not set, /proc/mounts will show the filesystem type as fuse/fuseblk.
	Subtype string

	// Flag to enable async reads that are received from
	// the kernel
	EnableAsyncReads bool

	// Flag to enable parallel lookup and readdir operations from the
	// kernel
	// Ref: https://github.com/torvalds/linux/commit/5c672ab3f0ee0f78f7acad183f34db0f8781a200
	EnableParallelDirOps bool

	// Flag to enable atomic truncate during file open operations.
	// When enabled, application calls to open with the O_TRUNC flag will cause a FUSE OpenFile
	// op with the O_TRUNC flag set. In comparison, the default behavior is an OpenFile op
	// without O_TRUNC, followed by a SetInodeAttributes op with the target size set to 0.
	// Ref: https://github.com/torvalds/linux/commit/6ff958edbf39c014eb06b65ad25b736be08c4e63
	EnableAtomicTrunc bool

	// Flag to tell the kernel we support ReadDirPlus, which optimizes performance
	// by returning not just the directory entries (like ReadDir), but also their inode
	// attributes, thereby saving one extra Lookup request per directory entry.
	EnableReaddirplus bool

	// Flag to enable adaptive ReadDirPlus.
	// This is only effective if EnableReaddirplus is true.
	//
	// When both flags are set, the kernel may dynamically choose between issuing
	// ReaddirPlus and Readdir requests based on observed access patterns.
	// For example, `ls` (which lists filenames only) may fall back to Readdir after an initial ReaddirPlus,
	// whereas `ls -l` is more likely to continue using ReaddirPlus.
	//
	// If EnableReaddirplus is true and this flag is false, the kernel will always
	// use ReaddirPlus for directory listing.
	EnableAutoReaddirplus bool
}

type FUSEImpl uint8

const (
	FUSEImplFuseT = iota
	FUSEImplMacFUSE
)

// Create a map containing all of the key=value mount options to be given to
// the mount helper.
func (c *MountConfig) toMap() (opts map[string]string) {
	isDarwin := runtime.GOOS == "darwin"
	opts = make(map[string]string)

	// Enable permissions checking in the kernel. See the comments on
	// InodeAttributes.Mode.
	if !c.DisableDefaultPermissions {
		opts["default_permissions"] = ""
	}

	// HACK(jacobsa): Work around what appears to be a bug in systemd v219, as
	// shipped in Ubuntu 15.04, where it automatically unmounts any file system
	// that doesn't set an explicit name.
	//
	// When Ubuntu contains systemd v220, this workaround should be removed and
	// the systemd bug reopened if the problem persists.
	//
	// Cf. https://github.com/bazil/fuse/issues/89
	// Cf. https://bugs.freedesktop.org/show_bug.cgi?id=90907
	fsname := c.FSName
	if runtime.GOOS == "linux" && fsname == "" {
		fsname = "some_fuse_file_system"
	}

	// Special file system name?
	if fsname != "" {
		opts["fsname"] = fsname
	}

	subtype := c.Subtype
	if subtype != "" {
		opts["subtype"] = subtype
	}

	// Read only?
	if c.ReadOnly {
		opts["ro"] = ""
	}

	// Handle OS X options.
	if isDarwin {
		if !c.EnableVnodeCaching {
			opts["novncache"] = ""
		}

		if c.VolumeName != "" {
			// Cf. https://github.com/osxfuse/osxfuse/wiki/Mount-options#volname
			opts["volname"] = c.VolumeName
		}
	}

	// OS X: disable the use of "Apple Double" (._foo and .DS_Store) files, which
	// just add noise to debug output and can have significant cost on
	// network-based file systems.
	//
	// Cf. https://github.com/osxfuse/osxfuse/wiki/Mount-options
	if isDarwin {
		opts["noappledouble"] = ""
	}

	// Last but not least: other user-supplied options.
	for k, v := range c.Options {
		opts[k] = v
	}

	return opts
}

func escapeOptionsKey(s string) (res string) {
	res = s
	res = strings.Replace(res, `\`, `\\`, -1)
	res = strings.Replace(res, `,`, `\,`, -1)
	return res
}

func mapToOptionsString(opts map[string]string) string {
	var components []string
	for k, v := range opts {
		k = escapeOptionsKey(k)

		component := k
		if v != "" {
			component = fmt.Sprintf("%s=%s", k, v)
		}

		components = append(components, component)
	}

	return strings.Join(components, ",")
}

// Create an options string suitable for passing to the mount helper.
func (c *MountConfig) toOptionsString() string {
	return mapToOptionsString(c.toMap())
}

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

import "errors"

var ErrExternallyManagedMountPoint = errors.New("externally managed mount point, skipping unmount")

// Unmount attempts to unmount the file system whose mount point is the
// supplied directory.
// For external mountpoints (like /dev/fd/N), it returns ErrExternallyManagedMountPoint
// for unsuccessful unmount attempt.
func Unmount(dir string) error {
	return unmount(dir)
}

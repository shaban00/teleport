/*
Copyright 2021 Gravitational, Inc.

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

package desktop

import (
	"sync"

	"github.com/gravitational/trace"
)

type directoryID uint32
type completionID uint32
type directoryName string

type readRequestInfo struct {
	directoryID directoryID
	path        string
	offset      uint64
}

type writeRequestInfo readRequestInfo

// maxAuditCacheItems is the maximum number of items we want
// to allow in a single sharedDirectoryAuditCacheEntry.
//
// It's not a precise value, just one that should prevent the
// cache from growing too large due to a misbehaving client.
const maxAuditCacheItems = 2000

// totalItems returns the total number of items held in the cache.
// The caller should hold a lock on the cache prior to calling this method.
func (e *sharedDirectoryAuditCache) totalItems() int {
	return len(e.nameCache) + len(e.readRequestCache) + len(e.writeRequestCache)
}

// sharedDirectoryAuditCache is a data structure for caching information
// from shared directory messages so that it can be used later for
// creating shared directory audit events.
type sharedDirectoryAuditCache struct {
	sync.Mutex

	nameCache         map[directoryID]directoryName
	readRequestCache  map[completionID]readRequestInfo
	writeRequestCache map[completionID]writeRequestInfo
}

func newSharedDirectoryAuditCache() sharedDirectoryAuditCache {
	return sharedDirectoryAuditCache{
		nameCache:         make(map[directoryID]directoryName),
		readRequestCache:  make(map[completionID]readRequestInfo),
		writeRequestCache: make(map[completionID]writeRequestInfo),
	}
}

// SetName returns a non-nil error if the audit cache entry for sid exceeds its maximum size.
// It is the responsibility of the caller to terminate the session if a non-nil error is returned.
func (c *sharedDirectoryAuditCache) SetName(did directoryID, name directoryName) error {
	c.Lock()
	defer c.Unlock()

	if c.totalItems() >= maxAuditCacheItems {
		return trace.LimitExceeded("audit cache exceeded maximum size")
	}

	c.nameCache[did] = name
	return nil
}

// SetReadRequestInfo returns a non-nil error if the audit cache exceeds its maximum size.
// It is the responsibility of the caller to terminate the session if a non-nil error is returned.
func (c *sharedDirectoryAuditCache) SetReadRequestInfo(cid completionID, info readRequestInfo) error {
	c.Lock()
	defer c.Unlock()

	if c.totalItems() >= maxAuditCacheItems {
		return trace.LimitExceeded("audit cache exceeded maximum size")
	}

	c.readRequestCache[cid] = info
	return nil
}

// SetWriteRequestInfo returns a non-nil error if the audit cache exceeds its maximum size.
// It is the responsibility of the caller to terminate the session if a non-nil error is returned.
func (c *sharedDirectoryAuditCache) SetWriteRequestInfo(cid completionID, info writeRequestInfo) error {
	c.Lock()
	defer c.Unlock()

	if c.totalItems() >= maxAuditCacheItems {
		return trace.LimitExceeded("audit cache exceeded maximum size")
	}

	c.writeRequestCache[cid] = info
	return nil
}

func (c *sharedDirectoryAuditCache) GetName(did directoryID) (name directoryName, ok bool) {
	c.Lock()
	defer c.Unlock()

	name, ok = c.nameCache[did]
	return
}

// TakeReadRequestInfo gets the readRequestInfo for completion ID cid,
// removing the readRequestInfo from the cache in the process.
func (c *sharedDirectoryAuditCache) TakeReadRequestInfo(cid completionID) (info readRequestInfo, ok bool) {
	c.Lock()
	defer c.Unlock()

	info, ok = c.readRequestCache[cid]
	if ok {
		delete(c.readRequestCache, cid)
	}
	return
}

// TakeWriteRequestInfo gets the writeRequestInfo for completion ID cid,
// removing the writeRequestInfo from the cache in the process.
func (c *sharedDirectoryAuditCache) TakeWriteRequestInfo(cid completionID) (info writeRequestInfo, ok bool) {
	c.Lock()
	defer c.Unlock()

	info, ok = c.writeRequestCache[cid]
	if ok {
		delete(c.writeRequestCache, cid)
	}
	return
}

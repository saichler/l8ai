/*
 * © 2025 Sharon Aicler (saichler@gmail.com)
 *
 * Layer 8 Ecosystem is licensed under the Apache License, Version 2.0.
 * You may obtain a copy of the License at:
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package common

import (
	"sync"
	"time"

	"github.com/saichler/l8types/go/ifs"
)

// UserCache caches Security().UserInfo per aaaId (plan §4.3). Only successful
// lookups are cached, so an expired session is rejected as soon as the entry
// ages out.
type UserCache struct {
	ttl     time.Duration
	mtx     sync.Mutex
	entries map[string]*userEntry
}

type userEntry struct {
	userId  string
	expires time.Time
}

// NewUserCache returns a cache that keeps each lookup for ttl.
func NewUserCache(ttl time.Duration) *UserCache {
	return &UserCache{ttl: ttl, entries: make(map[string]*userEntry)}
}

// Resolve returns the user ID behind a validated aaaId.
func (this *UserCache) Resolve(aaaId string, vnic ifs.IVNic) (string, error) {
	now := time.Now()
	this.mtx.Lock()
	entry, ok := this.entries[aaaId]
	this.mtx.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.userId, nil
	}
	userId, _, err := vnic.Resources().Security().UserInfo(aaaId, vnic)
	if err != nil {
		return "", err
	}
	this.mtx.Lock()
	this.entries[aaaId] = &userEntry{userId: userId, expires: now.Add(this.ttl)}
	this.mtx.Unlock()
	return userId, nil
}

// Cached returns the user of an aaaId resolved earlier, without a lookup.
func (this *UserCache) Cached(aaaId string) (string, bool) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	entry, ok := this.entries[aaaId]
	if !ok {
		return "", false
	}
	return entry.userId, true
}

// Purge drops expired entries. Entries are kept for one extra TTL so that
// Cached() can still map the aaaId of a recent subscriber to its user.
func (this *UserCache) Purge() {
	cutoff := time.Now().Add(-this.ttl)
	this.mtx.Lock()
	defer this.mtx.Unlock()
	for aaaId, entry := range this.entries {
		if entry.expires.Before(cutoff) {
			delete(this.entries, aaaId)
		}
	}
}

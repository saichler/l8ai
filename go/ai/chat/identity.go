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

package chat

import (
	"errors"
	"sync"
	"time"

	"github.com/saichler/l8types/go/ifs"
)

// userCache caches Security().UserInfo per aaaId (plan §4.3). Only successful
// lookups are cached, so an expired session is rejected as soon as the entry
// ages out.
type userCache struct {
	ttl     time.Duration
	mtx     sync.Mutex
	entries map[string]*userEntry
}

type userEntry struct {
	userId  string
	expires time.Time
}

func newUserCache(ttl time.Duration) *userCache {
	return &userCache{ttl: ttl, entries: make(map[string]*userEntry)}
}

// requester returns the user ID behind the aaaId of a browser request.
func (this *Instance) requester(msg *ifs.Message) (string, error) {
	aaaId := msg.AAAId()
	if aaaId == "" {
		return "", errors.New("chat: request has no session")
	}
	return this.users.resolve(aaaId, this.vnic)
}

func (this *userCache) resolve(aaaId string, vnic ifs.IVNic) (string, error) {
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

// cached returns the user of an aaaId resolved earlier, without a lookup.
func (this *userCache) cached(aaaId string) (string, bool) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	entry, ok := this.entries[aaaId]
	if !ok {
		return "", false
	}
	return entry.userId, true
}

// purge drops expired entries. Entries are kept for one extra TTL so that
// cached() can still map the aaaId of a recent subscriber to its user.
func (this *userCache) purge() {
	cutoff := time.Now().Add(-this.ttl)
	this.mtx.Lock()
	defer this.mtx.Unlock()
	for aaaId, entry := range this.entries {
		if entry.expires.Before(cutoff) {
			delete(this.entries, aaaId)
		}
	}
}

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

// Package users implements chat-users (plan §4.4, §5.5): presence (ChatStat)
// and groups (ChatGroup). Both are DCaches with a full copy on every replica;
// the web reaches them directly, not through the router.
package users

import (
	"errors"
	"runtime/debug"
	"sync"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
)

const sweepEvery = 30 * time.Second

// Users is one chat-users replica. The heartbeat map is local to the leader.
type Users struct {
	cfg   *aicommon.ChatConfig
	vnic  ifs.IVNic
	users *aicommon.UserCache

	mtx         sync.Mutex
	heartbeats  map[string]time.Time
	leaderSince time.Time
	loaded      bool
}

func newUsers(cfg *aicommon.ChatConfig, vnic ifs.IVNic) *Users {
	return &Users{cfg: cfg, vnic: vnic, users: aicommon.NewUserCache(cfg.UserInfoCacheTTL),
		heartbeats: make(map[string]time.Time)}
}

// requester returns the user behind a browser request's aaaId.
func (this *Users) requester(msg *ifs.Message) (string, error) {
	if msg.AAAId() == "" {
		return "", errors.New("chat-users: request has no session")
	}
	return this.users.Resolve(msg.AAAId(), this.vnic)
}

func (this *Users) handler(name string) ifs.IServiceHandlerCache {
	h, ok := this.vnic.Resources().Services().ServiceHandler(name, aicommon.ServiceArea)
	if !ok {
		return nil
	}
	cache, _ := h.(ifs.IServiceHandlerCache)
	return cache
}

// get reads one row of a DCache by its primary key.
func get(h ifs.IServiceHandler, filter interface{}, vnic ifs.IVNic) interface{} {
	resp := h.Get(object.New(nil, filter), vnic)
	if resp == nil || resp.Error() != nil {
		return nil
	}
	return resp.Element()
}

// sweep runs on every replica; only the leader acts. On taking over it loads
// the persisted custom statuses and groups, waits one full OfflineAfter window
// (its heartbeat map starts empty), then marks silent users offline (plan §5.5).
func (this *Users) sweep() {
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		<-ticker.C
		this.sweepTick()
	}
}

func (this *Users) sweepTick() {
	defer func() {
		if r := recover(); r != nil {
			this.vnic.Resources().Logger().Error("chat-users: sweep: ", r, "\n", string(debug.Stack()))
		}
	}()
	if !aicommon.IsLeader(this.vnic, aicommon.ChatStatService) {
		this.mtx.Lock()
		this.leaderSince = time.Time{}
		this.loaded = false
		this.heartbeats = make(map[string]time.Time)
		this.mtx.Unlock()
		return
	}
	now := time.Now()
	this.mtx.Lock()
	if this.leaderSince.IsZero() {
		this.leaderSince = now
	}
	loaded := this.loaded
	this.loaded = true
	since := this.leaderSince
	this.mtx.Unlock()
	if !loaded {
		this.loadStatuses()
		this.loadGroups()
	}
	if now.Sub(since) < this.cfg.OfflineAfter {
		return
	}
	stat := this.handler(aicommon.ChatStatService)
	if stat == nil {
		return
	}
	for _, v := range stat.All() {
		s, ok := v.(*l8ai.ChatUserStatus)
		if !ok || s.UserId == aicommon.TantoUserId || s.Presence == l8ai.ChatPresence_CHAT_PRESENCE_OFFLINE {
			continue
		}
		this.mtx.Lock()
		last := this.heartbeats[s.UserId]
		this.mtx.Unlock()
		if last.IsZero() || now.Sub(last) > this.cfg.OfflineAfter {
			s.Presence = l8ai.ChatPresence_CHAT_PRESENCE_OFFLINE
			stat.Post(object.New(nil, s), this.vnic)
		}
	}
}

func (this *Users) heartbeat(userId string) {
	this.mtx.Lock()
	this.heartbeats[userId] = time.Now()
	this.mtx.Unlock()
}

// loadStatuses reloads the custom statuses from ChatStatDb, as offline.
func (this *Users) loadStatuses() {
	stat := this.handler(aicommon.ChatStatService)
	elems, err := aicommon.LeaderGet(this.vnic, aicommon.ChatStatDbService, "select * from ChatUserStatus", this.cfg.ReadTimeoutSeconds)
	if err != nil || stat == nil {
		this.logLoad("statuses", err)
		return
	}
	for _, e := range elems {
		s, ok := e.(*l8ai.ChatUserStatus)
		if !ok || s.UserId == aicommon.TantoUserId || get(stat, &l8ai.ChatUserStatus{UserId: s.UserId}, this.vnic) != nil {
			continue
		}
		s.Presence = l8ai.ChatPresence_CHAT_PRESENCE_OFFLINE
		stat.Post(object.New(nil, s), this.vnic)
	}
}

// loadGroups reloads the groups from ChatGrpDb.
func (this *Users) loadGroups() {
	groups := this.handler(aicommon.ChatGroupService)
	elems, err := aicommon.LeaderGet(this.vnic, aicommon.ChatGrpDbService, "select * from ChatGroup", this.cfg.ReadTimeoutSeconds)
	if err != nil || groups == nil {
		this.logLoad("groups", err)
		return
	}
	for _, e := range elems {
		g, ok := e.(*l8ai.ChatGroup)
		if ok && get(groups, &l8ai.ChatGroup{GroupId: g.GroupId}, this.vnic) == nil {
			groups.Post(object.New(nil, g), this.vnic)
		}
	}
}

func (this *Users) logLoad(what string, err error) {
	if err != nil {
		this.vnic.Resources().Logger().Error("chat-users: load ", what, ": ", err.Error())
	}
}

// guard turns a panic into an error response.
func guard(vnic ifs.IVNic, resp *ifs.IElements) {
	if r := recover(); r != nil {
		vnic.Resources().Logger().Error("chat-users: recovered from panic: ", r, "\n", string(debug.Stack()))
		*resp = object.NewError("chat-users: internal error")
	}
}

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
	"sync"

	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
)

// ChatMessageServiceHandler is ChatShMsg, the chat message service. Like every
// chat instance service it holds no state: the service manager activates a new
// object of its type (Registry().NewOf), so the state lives on the shared
// Instance, in its messageStore, and the handler reaches it through the
// instance it finds in the SLA arguments (handlerBase).
type ChatMessageServiceHandler struct {
	handlerBase
}

// Activate finds the chat instance in the SLA arguments.
func (this *ChatMessageServiceHandler) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	return this.activate(sla)
}

// PostMessage handles a create: a user's message from the router, or Tanto's
// reply from a ChatAgent instance.
func (this *ChatMessageServiceHandler) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return this.inst.messages.postMessage(pb, vnic, msg)
}

// GetMessage handles a read from the router.
func (this *ChatMessageServiceHandler) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return this.inst.messages.getMessage(pb, vnic, msg)
}

// PutMessage, PatchMessage and DeleteMessage are rejected: messages are
// immutable time series elements.
func (this *ChatMessageServiceHandler) PutMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: messages are append-only")
}

func (this *ChatMessageServiceHandler) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: messages are append-only")
}

func (this *ChatMessageServiceHandler) DeleteMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: messages are append-only")
}

// messageStore is the chat instance's explicit store of ChatMessage: immutable
// time series elements whose only order is time. Each owned conversation has a
// window of its latest messages; older ones are read from chat-orm. It is the
// base from which BaseTimeSDataService (l8services) and TCache (l8utils) will
// be abstracted.
//
// Locks: a conversation's lock (c.mtx) is taken before the store's lock (mtx),
// never the other way around. mtx guards the conversations map and the windows
// and is held briefly, so writes to different conversations do not wait on
// each other.
type messageStore struct {
	// inst is the chat instance that owns the store and the conversations
	// (header, members, agent turn).
	inst *Instance
	mtx  *sync.RWMutex
	// conversations holds each conversation's window of latest messages, by
	// conversation ID.
	conversations map[string]*messageWindow
	// size is the window size: the latest messages kept per conversation
	// (ChatConfig.RingSize).
	size int
	// subs are the browsers' live registrations on conversation threads.
	subs *messageSubscriptions
	// notifySequence numbers the pushes this store creates.
	notifySequence uint32
}

// newMessageStore creates the instance's message store. The instance creates
// it with itself, so the reclaim at start-up already loads the owned
// conversations' windows into it, before the services are activated.
func newMessageStore(inst *Instance) *messageStore {
	this := &messageStore{inst: inst}
	this.init(inst.cfg.RingSize)
	return this
}

// init creates the lock, the conversations map and the subscriptions.
func (this *messageStore) init(size int) {
	this.mtx = &sync.RWMutex{}
	this.conversations = make(map[string]*messageWindow)
	this.size = size
	this.subs = newMessageSubscriptions()
}

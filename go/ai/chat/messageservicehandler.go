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

	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
)

// ChatMessageServiceHandler is ChatShMsg, the chat message service, with its
// own, explicit store of ChatMessage: immutable time series elements whose
// only order is time. Each owned conversation has a window of its latest
// messages; older ones are read from chat-orm. It is the base from which
// BaseTimeSDataService (l8services) and TCache (l8utils) will be abstracted.
//
// Locks: a conversation's lock (c.mtx) is taken before the handler's lock
// (mtx), never the other way around. mtx guards the conversations map and
// the windows and is held briefly, so writes to different conversations do
// not wait on each other.
type ChatMessageServiceHandler struct {
	// handlerBase holds the shared chat instance (the conversations: header,
	// members, agent turn) and the IServiceHandler methods every chat instance
	// service shares.
	handlerBase
	mtx *sync.RWMutex
	// conversations holds each conversation's window of latest messages, by
	// conversation ID.
	conversations map[string]*messageWindow
	// size is the window size: the latest messages kept per conversation
	// (ChatConfig.RingSize).
	size int
	// subs are the browsers' live registrations on conversation threads.
	subs *messageSubscriptions
	// notifySequence numbers the pushes this handler creates.
	notifySequence uint32
}

// newChatMessageServiceHandler creates the instance's message handler. The
// instance creates it with itself, so the reclaim at start-up already loads
// the owned conversations' windows through it, before the services are
// activated.
func newChatMessageServiceHandler(inst *Instance) *ChatMessageServiceHandler {
	this := &ChatMessageServiceHandler{}
	this.inst = inst
	this.init(inst.cfg.RingSize)
	return this
}

// Activate checks that the SLA activates the instance's own message handler:
// the instance and the service must share one store of message windows.
func (this *ChatMessageServiceHandler) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	inst := this.inst
	if err := this.activate(sla); err != nil {
		return err
	}
	if this.inst != inst || this.inst.messages != this {
		return errors.New("chat message service activated with a handler that is not its instance's")
	}
	return nil
}

// init creates the lock, the conversations map and the subscriptions.
func (this *ChatMessageServiceHandler) init(size int) {
	this.mtx = &sync.RWMutex{}
	this.conversations = make(map[string]*messageWindow)
	this.size = size
	this.subs = newMessageSubscriptions()
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

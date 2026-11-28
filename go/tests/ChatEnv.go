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

package tests

import (
	"github.com/google/uuid"
	"github.com/saichler/l8ai/go/ai/chat"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/ai/orm"
	"github.com/saichler/l8ai/go/ai/router"
	"github.com/saichler/l8ai/go/ai/users"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
)

// chatEnv is the chat stack on the test topology, as probler deploys it: the
// chat-orm services, the users service, a chat instance and a router, plus a
// client node that calls the router's public services the way the web server
// does (with the aaaId of a logged-in session).
type chatEnv struct {
	orm, users, chat, router, client ifs.IVNic
	instance                         *chat.Instance
}

// The chat store of the probler security configuration (sysconfig
// chatStoreConfig): credential "postgres", database "problerchat".
const (
	chatStoreCreds = "postgres"
	chatStoreDb    = "problerchat"
	requestTimeout = 15
)

func startChatEnv(cfg *aicommon.ChatConfig) *chatEnv {
	env := &chatEnv{
		orm:    topo.VnicByVnetNum(1, 1),
		users:  topo.VnicByVnetNum(1, 2),
		chat:   topo.VnicByVnetNum(2, 1),
		router: topo.VnicByVnetNum(2, 2),
		client: topo.VnicByVnetNum(3, 1),
	}
	aicommon.RegisterTypes(env.client.Resources())
	orm.ActivateOrm(chatStoreCreds, chatStoreDb, env.orm)
	users.ActivateUsers(cfg, env.users)
	env.instance = chat.ActivateChat(cfg, env.chat)
	router.ActivateRouter(cfg, env.router)
	activateWsCapture(env.client)
	return env
}

// login authenticates a user of the probler security configuration and
// returns the session's aaaId, as the web server resolves a bearer token.
func (this *chatEnv) login(user, pass string) string {
	sec := this.client.Resources().Security()
	token := sec.Authenticate(user, pass, this.client)
	if token == nil || token.Token == "" {
		panic("login " + user + " failed")
	}
	aaaId, ok := sec.ValidateToken(token.Token, this.client)
	if !ok {
		panic("token of " + user + " is not valid")
	}
	return aaaId
}

// call sends a request to one of the router's public chat services.
func (this *chatEnv) call(service string, action ifs.Action, body interface{}, aaaId string) ifs.IElements {
	return this.client.ProximityRequest(service, aicommon.ServiceArea, action, body, requestTimeout, aaaId)
}

// read sends an L8Query to one of the router's public chat services. Like
// the web server, it stamps the session's aaaId on the query.
func (this *chatEnv) read(service, text, aaaId string) ifs.IElements {
	return this.call(service, ifs.GET, &l8api.L8Query{Text: text, AaaId: aaaId}, aaaId)
}

// createDM creates a direct conversation between two users and returns its ID.
func (this *chatEnv) createDM(aaaId, user, peer string) (string, ifs.IElements) {
	id := uuid.NewString()
	resp := this.call(aicommon.ChatConvService, ifs.POST, &l8ai.ChatConversation{ConversationId: id,
		PeerKind: l8ai.ChatPeerKind_CHAT_PEER_KIND_USER, PeerKey: user + "|" + peer, Title: "dm " + id[:8]}, aaaId)
	return id, resp
}

// send posts a text message to a conversation and returns its ID.
func (this *chatEnv) send(aaaId, conversationId, body string) (string, ifs.IElements) {
	id := uuid.NewString()
	resp := this.call(aicommon.ChatMsgService, ifs.POST, &l8ai.ChatMessage{MessageId: id,
		ConversationId: conversationId, Body: body}, aaaId)
	return id, resp
}

// messages returns the ChatMessage elements of a response.
func messages(resp ifs.IElements) []*l8ai.ChatMessage {
	result := make([]*l8ai.ChatMessage, 0)
	if resp == nil {
		return result
	}
	for _, e := range resp.Elements() {
		if m, ok := e.(*l8ai.ChatMessage); ok && m != nil {
			result = append(result, m)
		}
	}
	return result
}

// seqsOf returns the seqs of messages, for failure messages.
func seqsOf(list []*l8ai.ChatMessage) []int64 {
	result := make([]int64, len(list))
	for i, m := range list {
		result[i] = m.Seq
	}
	return result
}

// register reads a page with Register, so the client component registerId of
// the session gets live pushes for what it displays.
func (this *chatEnv) register(text, aaaId, registerId string) ifs.IElements {
	return this.call(aicommon.ChatMsgService, ifs.GET, &l8api.L8Query{Text: text, AaaId: aaaId,
		Register: l8api.L8Register_L8_REGISTER_REGISTER, RegisterId: registerId}, aaaId)
}

// unregister removes the client component's live registration.
func (this *chatEnv) unregister(text, aaaId, registerId string) ifs.IElements {
	return this.call(aicommon.ChatMsgService, ifs.GET, &l8api.L8Query{Text: text, AaaId: aaaId,
		Register: l8api.L8Register_L8_REGISTER_UNREGISTER, RegisterId: registerId}, aaaId)
}

// direct sends a request straight to the chat instance's ChatShMsg, bypassing
// the router.
func (this *chatEnv) direct(action ifs.Action, body interface{}, aaaId string) ifs.IElements {
	return this.client.Request(this.chat.Resources().SysConfig().LocalUuid, aicommon.ChatShMsgService,
		aicommon.ServiceArea, action, body, requestTimeout, aaaId)
}

// header reads a conversation's header through the router.
func (this *chatEnv) header(aaaId, conversationId string) *l8ai.ChatConversation {
	resp := this.read(aicommon.ChatConvService, "select * from ChatConversation where conversationId="+conversationId, aaaId)
	if resp == nil {
		return nil
	}
	for _, e := range resp.Elements() {
		if h, ok := e.(*l8ai.ChatConversation); ok && h != nil && h.ConversationId == conversationId {
			return h
		}
	}
	return nil
}

// failed reports whether a response is missing or carries an error.
func failed(resp ifs.IElements) bool {
	return resp == nil || resp.Error() != nil
}

// errOf returns a response's error text, for failure messages.
func errOf(resp ifs.IElements) string {
	if resp == nil {
		return "no response"
	}
	if resp.Error() != nil {
		return resp.Error().Error()
	}
	return "ok"
}

// capturedCount returns how many pushes the websocket stub recorded.
func capturedCount() int {
	captured.mtx.Lock()
	defer captured.mtx.Unlock()
	return len(captured.sets)
}

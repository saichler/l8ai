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

// Package turn is the ChatAgent service (plan §7.4): it receives a turn from
// the conversation's owner chat instance, runs the LLM tool loop as the
// requesting user, posts Tanto's reply back to that instance, and compacts
// the conversation after the reply.
package turn

import (
	"sync"

	"github.com/saichler/l8ai/go/ai/agent/catalog"
	"github.com/saichler/l8ai/go/ai/agent/executor"
	"github.com/saichler/l8ai/go/ai/agent/llm"
	"github.com/saichler/l8ai/go/ai/agent/masking"
	"github.com/saichler/l8ai/go/ai/agent/tools"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8types/go/ifs"
)

// Settings are the agent's resolved settings.
type Settings struct {
	SystemPrompt       string // appended to DefaultSystemPrompt
	HelpDocs           string // '@help' documentation; empty disables the mode's answers
	MaxToolCalls       int
	MaxConcurrentTurns int
	ToolTimeoutSeconds int // bound of one tool call's service request
	Chat               *aicommon.ChatConfig
}

// Agent is one agent instance: the parts every turn uses.
type Agent struct {
	settings *Settings
	vnic     ifs.IVNic
	catalog  *catalog.Catalog
	exec     *executor.Executor
	factory  *llm.Factory
	masks    masking.Resolver
	secret   []byte
	web      *tools.Web

	slots chan struct{} // MaxConcurrentTurns

	mtx        sync.Mutex
	running    map[string]bool // turns in flight here, by turn ID
	compacting map[string]bool // conversations being compacted here
}

// NewAgent builds an agent from its parts.
func NewAgent(settings *Settings, vnic ifs.IVNic, c *catalog.Catalog, factory *llm.Factory,
	secret []byte, web *tools.Web) *Agent {
	RegisterFileTypes(vnic.Resources().Registry())
	return &Agent{settings: settings, vnic: vnic, catalog: c,
		exec:    executor.New(vnic, c, settings.ToolTimeoutSeconds),
		factory: factory, masks: masking.RegistryResolver(vnic.Resources().Registry()),
		secret: secret, web: web,
		slots:   make(chan struct{}, settings.MaxConcurrentTurns),
		running: make(map[string]bool), compacting: make(map[string]bool)}
}

// begin marks a turn as running here; false if it already is (a re-dispatch
// of a turn this instance is still working on).
func (this *Agent) begin(turnId string) bool {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	if this.running[turnId] {
		return false
	}
	this.running[turnId] = true
	return true
}

func (this *Agent) end(turnId string) {
	this.mtx.Lock()
	delete(this.running, turnId)
	this.mtx.Unlock()
}

// startCompaction allows one compaction per conversation at a time here; the
// owner's compare-and-set settles races between agent instances.
func (this *Agent) startCompaction(conversationId string) bool {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	if this.compacting[conversationId] {
		return false
	}
	this.compacting[conversationId] = true
	return true
}

func (this *Agent) endCompaction(conversationId string) {
	this.mtx.Lock()
	delete(this.compacting, conversationId)
	this.mtx.Unlock()
}

func (this *Agent) budget() int {
	return (this.settings.Chat.ContextWindowTokens - this.settings.Chat.MaxOutputTokens) * 95 / 100
}

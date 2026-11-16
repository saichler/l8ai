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

// Package agent activates the Tanto agent (plan §7): the ChatAgent service,
// its LLM clients, the cluster-wide model catalog, masking and the tools.
package agent

import (
	"crypto/rand"
	"time"

	"github.com/saichler/l8ai/go/ai/agent/catalog"
	"github.com/saichler/l8ai/go/ai/agent/llm"
	"github.com/saichler/l8ai/go/ai/agent/masking"
	"github.com/saichler/l8ai/go/ai/agent/tools"
	"github.com/saichler/l8ai/go/ai/agent/turn"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
)

// The ChatAgent service receives the incoming message (F6).
var _ ifs.IServiceMessageHandler = (*turn.Service)(nil)

// Defaults of AgentConfig.
const (
	DefaultMaxToolCalls       = 10
	DefaultMaxConcurrentTurns = 64
	DefaultToolTimeoutSeconds = 30
	catalogRefreshEvery       = 30 * time.Second
)

// AgentConfig configures an agent instance.
type AgentConfig struct {
	// RegisterTypes registers the adopter's models, as its web server does,
	// so describe_model and create/update can read them.
	RegisterTypes func(ifs.IResources)
	// SystemPrompt is appended to turn.DefaultSystemPrompt (adopter domain hints).
	SystemPrompt string
	// HelpDocs is the markdown answered by '@help' prompts.
	HelpDocs string
	// WebToolsURL is the web tools service of '@web' prompts; empty answers
	// them from model knowledge.
	WebToolsURL string
	// ServiceMap overrides or adds model → service entries of the catalog (N6).
	ServiceMap map[string]catalog.ServiceRef
	// MaskingOverrides overrides field classifications per model.
	MaskingOverrides masking.OverrideFunc
	// MaskingSecret keys the deterministic mask tokens. It must be the same on
	// every agent instance so tokens are stable across instances; when empty a
	// random secret is used and tokens are stable within this instance only.
	MaskingSecret string
	// LlmRequestsPerMinute is each provider's quota, shared by the live agent
	// instances (0: unlimited).
	LlmRequestsPerMinute int
	MaxToolCalls         int
	MaxConcurrentTurns   int
	ToolTimeoutSeconds   int
	// Chat holds the shared limits (turn timeout, context window, output and
	// tool result tokens, compaction); nil uses the defaults.
	Chat *aicommon.ChatConfig
}

// ActivateAgent activates a ChatAgent instance on the vnic.
func ActivateAgent(cfg AgentConfig, vnic ifs.IVNic) *turn.Agent {
	res := vnic.Resources()
	aicommon.RegisterTypes(res)
	if cfg.RegisterTypes != nil {
		cfg.RegisterTypes(res)
	}
	settings := &turn.Settings{SystemPrompt: cfg.SystemPrompt, HelpDocs: cfg.HelpDocs,
		MaxToolCalls:       orDefault(cfg.MaxToolCalls, DefaultMaxToolCalls),
		MaxConcurrentTurns: orDefault(cfg.MaxConcurrentTurns, DefaultMaxConcurrentTurns),
		ToolTimeoutSeconds: orDefault(cfg.ToolTimeoutSeconds, DefaultToolTimeoutSeconds),
		Chat:               cfg.Chat}
	if settings.Chat == nil {
		settings.Chat = aicommon.NewChatConfig()
	}
	secret := []byte(cfg.MaskingSecret)
	if len(secret) == 0 {
		res.Logger().Warning("ChatAgent: no MaskingSecret; mask tokens are stable on this instance only")
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
	}
	c := catalog.New(res, cfg.ServiceMap)
	factory := llm.NewFactory()
	a := turn.NewAgent(settings, vnic, c, factory, masking.NewClassifier(cfg.MaskingOverrides), secret,
		tools.NewWeb(cfg.WebToolsURL))

	// Activated as stateful so the chat instances find it with GetParticipants (DR30).
	vnic.Resources().Registry().Register(&turn.Service{})
	sla := ifs.NewServiceLevelAgreement(&turn.Service{}, aicommon.ChatAgentService, aicommon.ServiceArea, true, nil)
	sla.SetServiceItem(&l8ai.ChatAgentTurn{})
	sla.SetPrimaryKeys("TurnId")
	sla.SetArgs(a)
	if _, err := vnic.Resources().Services().Activate(sla, vnic); err != nil {
		res.Logger().Error("ChatAgent: activate: ", err.Error())
	}

	go refresh(vnic, c, factory, cfg.LlmRequestsPerMinute)
	return a
}

// refresh keeps the catalog current (services start on other nodes after
// this one) and splits the provider quota over the live agent instances.
func refresh(vnic ifs.IVNic, c *catalog.Catalog, factory *llm.Factory, quotaPerMinute int) {
	for {
		if quotaPerMinute > 0 {
			instances := len(vnic.Resources().Services().GetParticipants(aicommon.ChatAgentService, aicommon.ServiceArea))
			if instances < 1 {
				instances = 1
			}
			factory.SetRate(float64(quotaPerMinute) / float64(instances))
		}
		time.Sleep(catalogRefreshEvery)
		c.Refresh()
	}
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

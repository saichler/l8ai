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

package llm

import (
	"errors"
	"sync"
)

// Factory builds clients from LLM profiles and caches them by kind, base URL,
// API key and model, so a changed profile builds a new client. Clients of the
// same provider (base URL) share one request limiter.
type Factory struct {
	mtx      sync.Mutex
	clients  map[Profile]Client
	limiters map[string]*limiter
	perMin   float64 // requests per minute this instance may send per provider
	ends     *Endpoints
}

// NewFactory returns an empty client factory whose clients reach each
// provider through endpoints (nil: its base URL as is).
func NewFactory(endpoints *Endpoints) *Factory {
	return &Factory{clients: make(map[Profile]Client), limiters: make(map[string]*limiter), ends: endpoints}
}

// Client returns the client of a profile.
func (this *Factory) Client(p *Profile) (Client, error) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	if c, ok := this.clients[*p]; ok {
		return c, nil
	}
	l, ok := this.limiters[p.BaseURL]
	if !ok {
		l = newLimiter()
		l.SetRate(this.perMin)
		this.limiters[p.BaseURL] = l
	}
	var c Client
	switch p.Kind {
	case KindAnthropic:
		c = newAnthropic(p, l, this.ends)
	case KindOpenAICompatible:
		c = newOpenAI(p, l, this.ends)
	default:
		return nil, errors.New("unsupported LLM provider kind " + p.Kind)
	}
	this.clients[*p] = c
	return c, nil
}

// SetRate sets each provider's requests per minute for this instance:
// the provider quota divided by the live agent instances (assessment §2.2).
func (this *Factory) SetRate(perMinute float64) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.perMin = perMinute
	for _, l := range this.limiters {
		l.SetRate(perMinute)
	}
}

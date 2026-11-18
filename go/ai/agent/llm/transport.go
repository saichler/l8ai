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
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	requestTimeout = 10 * time.Minute
	maxRetries     = 3
	baseBackoff    = time.Second
	maxBackoff     = 30 * time.Second
	// limiterWait is how long a call waits for the provider's limiter before
	// failing, which applies backpressure (assessment §2.2).
	limiterWait = 30 * time.Second
)

// poster sends JSON to a provider with retry on 429 and 5xx (honouring
// Retry-After, exponential backoff with jitter) behind a request limiter.
// The URL goes through the endpoints, so it is sent to the main URL or its
// fallback, whichever answers.
type poster struct {
	http      *http.Client
	limiter   *limiter
	endpoints *Endpoints
}

func newPoster(l *limiter, e *Endpoints) *poster {
	return &poster{http: e.HTTPClient(requestTimeout), limiter: l, endpoints: e}
}

func (this *poster) post(ctx context.Context, url string, headers map[string]string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := this.limiter.wait(ctx); err != nil {
			return nil, err
		}
		target := this.endpoints.Resolve(ctx, url)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := this.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() == nil {
				this.endpoints.Failed(target)
			}
			if !sleep(ctx, backoff(attempt, "")) {
				return nil, ctx.Err()
			}
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusOK {
			return data, nil
		}
		lastErr = errors.New("LLM API error (" + resp.Status + "): " + string(data))
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return nil, lastErr
		}
		if !sleep(ctx, backoff(attempt, resp.Header.Get("Retry-After"))) {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func backoff(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	d := baseBackoff << attempt
	if d > maxBackoff {
		d = maxBackoff
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// limiter is a token bucket of requests per minute for one provider. The
// rate is the provider's quota divided by the live agent instances, and can
// change at any time. A rate of 0 means unlimited.
type limiter struct {
	mtx      sync.Mutex
	perMin   float64
	tokens   float64
	lastFill time.Time
}

func newLimiter() *limiter {
	return &limiter{lastFill: time.Now()}
}

// SetRate sets the requests per minute this instance may send.
func (this *limiter) SetRate(perMinute float64) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.perMin = perMinute
	if this.tokens > perMinute {
		this.tokens = perMinute
	}
}

func (this *limiter) wait(ctx context.Context) error {
	deadline := time.Now().Add(limiterWait)
	for {
		this.mtx.Lock()
		if this.perMin <= 0 {
			this.mtx.Unlock()
			return nil
		}
		now := time.Now()
		this.tokens += now.Sub(this.lastFill).Minutes() * this.perMin
		if this.tokens > this.perMin {
			this.tokens = this.perMin
		}
		this.lastFill = now
		if this.tokens >= 1 {
			this.tokens--
			this.mtx.Unlock()
			return nil
		}
		need := time.Duration((1 - this.tokens) / this.perMin * float64(time.Minute))
		this.mtx.Unlock()
		if time.Now().Add(need).After(deadline) {
			return errors.New("the LLM provider's request limit is reached; try again shortly")
		}
		if !sleep(ctx, need) {
			return ctx.Err()
		}
	}
}

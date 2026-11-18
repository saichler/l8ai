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
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProbeTimeout is how long the main URL of a service has to answer before
// its fallback is used.
const ProbeTimeout = 5 * time.Second

// Endpoints chooses, per service, between its main URL and a fallback (e.g.
// the LAN address of the same service): the main URL is probed on first use
// and, when it does not answer within ProbeTimeout, the fallback is used. A
// request that cannot reach the chosen URL makes the next one probe again, so
// a machine that moves between networks follows on its own.
//
// Services are matched by origin (scheme://host[:port]); the path of a
// request is kept, so "https://llm.example.com" → "https://10.0.0.5:8080"
// sends https://llm.example.com/v1/chat/completions to
// https://10.0.0.5:8080/v1/chat/completions. A nil *Endpoints, or a service
// without a fallback, is used as is.
type Endpoints struct {
	fallbacks map[string]string // main origin → fallback origin
	tls       *tls.Config
	timeout   time.Duration
	probe     func(ctx context.Context, origin string) error
	log       func(msg string)

	mtx    sync.Mutex
	chosen map[string]string      // main origin → origin in use
	locks  map[string]*sync.Mutex // one probe at a time per main origin
}

// NewEndpoints returns the resolver of fallbacks (main URL → fallback URL;
// only their origins count). trustedCAs is PEM certificates trusted in
// addition to the system ones, for fallbacks serving a private CA's
// certificate. log, when set, is told which URL each service uses.
func NewEndpoints(fallbacks map[string]string, trustedCAs string, log func(msg string)) (*Endpoints, error) {
	this := &Endpoints{fallbacks: make(map[string]string), timeout: ProbeTimeout, log: log,
		chosen: make(map[string]string), locks: make(map[string]*sync.Mutex)}
	for main, fallback := range fallbacks {
		m, f := originOf(main), originOf(fallback)
		if m == "" || f == "" {
			return nil, errors.New("invalid endpoint fallback " + main + " → " + fallback)
		}
		this.fallbacks[m] = f
	}
	if strings.TrimSpace(trustedCAs) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(trustedCAs)) {
			return nil, errors.New("no certificate in the trusted CAs")
		}
		this.tls = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	this.probe = this.httpProbe
	return this, nil
}

// TLSConfig is the TLS configuration of clients calling these endpoints (nil:
// the system defaults).
func (this *Endpoints) TLSConfig() *tls.Config {
	if this == nil || this.tls == nil {
		return nil
	}
	return this.tls.Clone()
}

// HTTPClient returns a client with the given timeout that trusts the
// configured CAs.
func (this *Endpoints) HTTPClient(timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if cfg := this.TLSConfig(); cfg != nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = cfg
		c.Transport = t
	}
	return c
}

// Resolve returns rawURL on the origin in use for its service, probing the
// main origin first if it was not chosen yet.
func (this *Endpoints) Resolve(ctx context.Context, rawURL string) string {
	if this == nil {
		return rawURL
	}
	main := originOf(rawURL)
	fallback, ok := this.fallbacks[main]
	if !ok {
		return rawURL
	}
	this.mtx.Lock()
	lock, ok := this.locks[main]
	if !ok {
		lock = &sync.Mutex{}
		this.locks[main] = lock
	}
	this.mtx.Unlock()

	lock.Lock()
	defer lock.Unlock()
	this.mtx.Lock()
	use, ok := this.chosen[main]
	this.mtx.Unlock()
	if !ok {
		use = main
		if err := this.probe(ctx, main); err != nil {
			if ctx.Err() != nil {
				return rawURL // the caller gave up; decide on the next call
			}
			use = fallback
			this.logf(main + " did not answer (" + err.Error() + "): using " + fallback)
		} else {
			this.logf("using " + main)
		}
		this.mtx.Lock()
		this.chosen[main] = use
		this.mtx.Unlock()
	}
	return use + pathOf(rawURL)
}

// Failed reports that rawURL (as returned by Resolve) could not be reached,
// so the next request probes the main URL again.
func (this *Endpoints) Failed(rawURL string) {
	if this == nil {
		return
	}
	origin := originOf(rawURL)
	this.mtx.Lock()
	defer this.mtx.Unlock()
	for main, use := range this.chosen {
		if use == origin {
			delete(this.chosen, main)
		}
	}
}

// ProbeAll chooses the URL of every service with a fallback now, so the
// first request does not wait for the probe.
func (this *Endpoints) ProbeAll(ctx context.Context) {
	if this == nil {
		return
	}
	var wg sync.WaitGroup
	for main := range this.fallbacks {
		wg.Add(1)
		go func(main string) {
			defer wg.Done()
			this.Resolve(ctx, main)
		}(main)
	}
	wg.Wait()
}

// httpProbe succeeds when the origin answers HTTP at all (any status: a
// 401 or 404 still shows the service is reachable).
func (this *Endpoints) httpProbe(ctx context.Context, origin string) error {
	ctx, cancel := context.WithTimeout(ctx, this.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/", nil)
	if err != nil {
		return err
	}
	resp, err := this.HTTPClient(this.timeout).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (this *Endpoints) logf(msg string) {
	if this.log != nil {
		this.log("LLM endpoints: " + msg)
	}
}

// originOf returns scheme://host[:port] of rawURL, lower-cased, or "".
func originOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// pathOf returns what follows the origin in rawURL (path, query, fragment).
func pathOf(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	rest := rawURL[strings.Index(rawURL, "://")+3:]
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		return rest[i:]
	}
	return ""
}

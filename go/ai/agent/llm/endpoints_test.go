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
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// silentServer accepts connections and never answers, like a public address
// that is unreachable from this network.
func silentServer(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	return "https://" + l.Addr().String()
}

func caPEM(srv *httptest.Server) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

func TestEndpointsFallBackOnTimeoutAndKeepPath(t *testing.T) {
	fallback := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fallback " + r.URL.Path))
	}))
	defer fallback.Close()
	main := silentServer(t)
	e, err := NewEndpoints(map[string]string{main + "/v1": fallback.URL}, caPEM(fallback), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.timeout = 300 * time.Millisecond

	start := time.Now()
	got := e.Resolve(context.Background(), main+"/v1/chat/completions")
	if want := fallback.URL + "/v1/chat/completions"; got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the probe took %v, longer than its timeout", d)
	}
	// The choice is kept: no second probe.
	start = time.Now()
	e.Resolve(context.Background(), main+"/v1/x")
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("the second request probed again (%v)", d)
	}
	// The fallback's private CA is trusted.
	resp, err := e.HTTPClient(time.Second).Get(got)
	if err != nil {
		t.Fatalf("the fallback is not reachable with the trusted CA: %v", err)
	}
	resp.Body.Close()
}

func TestEndpointsUseMainWhenItAnswers(t *testing.T) {
	main := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // any answer counts
	}))
	defer main.Close()
	e, err := NewEndpoints(map[string]string{main.URL: "https://10.255.255.1:8080"}, caPEM(main), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Resolve(context.Background(), main.URL+"/v1/search"); got != main.URL+"/v1/search" {
		t.Fatalf("Resolve = %q, want the main URL", got)
	}
}

func TestEndpointsReprobeAfterFailure(t *testing.T) {
	e, _ := NewEndpoints(map[string]string{"https://llm.example.com": "https://10.0.0.5:8080"}, "", nil)
	up := false
	probes := 0
	e.probe = func(ctx context.Context, origin string) error {
		probes++
		if origin != "https://llm.example.com" {
			t.Fatalf("probed %q, not the main origin", origin)
		}
		if up {
			return nil
		}
		return errors.New("timeout")
	}
	ctx := context.Background()
	got := e.Resolve(ctx, "https://LLM.example.com/v1/chat/completions")
	if got != "https://10.0.0.5:8080/v1/chat/completions" {
		t.Fatalf("Resolve = %q, want the fallback", got)
	}
	e.Resolve(ctx, "https://llm.example.com/v1")
	if probes != 1 {
		t.Fatalf("probed %d times, want once", probes)
	}
	// Back on the other network: the fallback fails, the main one answers.
	up = true
	e.Failed(got)
	if got = e.Resolve(ctx, "https://llm.example.com/v1"); got != "https://llm.example.com/v1" {
		t.Fatalf("after a failure Resolve = %q, want the main URL again", got)
	}
	if probes != 2 {
		t.Fatalf("probed %d times, want twice", probes)
	}
}

func TestEndpointsWithoutFallback(t *testing.T) {
	var nilEndpoints *Endpoints
	if got := nilEndpoints.Resolve(context.Background(), "https://a.example/v1"); got != "https://a.example/v1" {
		t.Fatalf("nil Resolve = %q", got)
	}
	nilEndpoints.Failed("https://a.example")
	if nilEndpoints.HTTPClient(time.Second) == nil {
		t.Fatal("nil HTTPClient")
	}
	e, _ := NewEndpoints(map[string]string{"https://llm.example.com": "https://10.0.0.5:8080"}, "", nil)
	e.probe = func(context.Context, string) error { t.Fatal("probed a service without a fallback"); return nil }
	if got := e.Resolve(context.Background(), "https://other.example.com/v1"); got != "https://other.example.com/v1" {
		t.Fatalf("Resolve = %q", got)
	}
}

func TestNewEndpointsRejectsBadInput(t *testing.T) {
	if _, err := NewEndpoints(map[string]string{"llm.example.com": "https://10.0.0.5"}, "", nil); err == nil {
		t.Fatal("a main URL without a scheme was accepted")
	}
	if _, err := NewEndpoints(nil, "not a certificate", nil); err == nil ||
		!strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a bad CA was accepted: %v", err)
	}
}

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

package turn

import "testing"

// The @web gate passes a question only on a clear NO and fails closed on
// anything else, including the empty answer a reasoning model gives when its
// token budget runs out mid-thought.
func TestClassifiedGeneral(t *testing.T) {
	cases := map[string]bool{
		"NO":         true,
		"No.":        true,
		"no":         true,
		"**NO**":     true,
		"  NO\n":     true,
		"YES":        false,
		"**YES**":    false,
		"":           false,
		"   ":        false,
		"Not sure":   false,
		"NOPE":       false,
		"Maybe, no.": false,
	}
	for answer, want := range cases {
		if got := classifiedGeneral(answer); got != want {
			t.Errorf("classifiedGeneral(%q) = %v, want %v", answer, got, want)
		}
	}
	if classifierMaxTokens < 512 {
		t.Errorf("classifierMaxTokens = %d leaves a reasoning model no room to answer", classifierMaxTokens)
	}
}

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

package router

import (
	"reflect"
	"sort"
	"testing"

	aicommon "github.com/saichler/l8ai/go/ai/common"
)

const mb = 1 << 20

// steady returns n identical samples.
func steady(n int, mem uint64, cpu float64) []healthSample {
	list := make([]healthSample, n)
	for i := range list {
		list[i] = healthSample{mem: mem, cpu: cpu}
	}
	return list
}

func run(samples map[string][]healthSample) decision {
	ids := make([]string, 0, len(samples))
	for id := range samples {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return decide(ids, samples, aicommon.NewChatConfig())
}

func expect(t *testing.T, d decision, donors, receivers []string) {
	t.Helper()
	if !reflect.DeepEqual(d.donors, donors) || !reflect.DeepEqual(d.receivers, receivers) {
		t.Fatalf("donors %v receivers %v, expected donors %v receivers %v (mem=%v cpu=%v)",
			d.donors, d.receivers, donors, receivers, d.memImbalanced, d.cpuImbalanced)
	}
}

// One instance at 1GB, its peer at 200MB: the memory flows to the peer.
func TestRebalanceOneHeavyInstance(t *testing.T) {
	d := run(map[string][]healthSample{
		"a": steady(5, 1024*mb, 5),
		"b": steady(5, 200*mb, 5),
	})
	expect(t, d, []string{"a"}, []string{"b"})
}

// Three instances at a few GB, two new ones at a few hundred MB: the old
// instances give to the new ones (a ratio against the average of the others
// would miss this; the lowest instance is the reference).
func TestRebalanceNewReplicas(t *testing.T) {
	d := run(map[string][]healthSample{
		"a": steady(5, 3000*mb, 20),
		"b": steady(5, 3100*mb, 20),
		"c": steady(5, 2900*mb, 20),
		"d": steady(5, 300*mb, 1),
		"e": steady(5, 250*mb, 1),
	})
	expect(t, d, []string{"a", "b", "c"}, []string{"d", "e"})
}

// Two instances carry everything but are close to each other; the third is
// empty but the gaps are below the noise floor: nothing moves.
func TestRebalanceBelowNoiseFloor(t *testing.T) {
	d := run(map[string][]healthSample{
		"a": steady(5, 150*mb, 0.5),
		"b": steady(5, 160*mb, 0.6),
		"c": steady(5, 40*mb, 0.1),
	})
	expect(t, d, nil, nil)
}

// Balanced instances with large usage: the ratio is not met, nothing moves.
func TestRebalanceBalanced(t *testing.T) {
	d := run(map[string][]healthSample{
		"a": steady(5, 3000*mb, 40),
		"b": steady(5, 2600*mb, 35),
	})
	expect(t, d, nil, nil)
}

// The imbalance must hold in every one of the samples: one balanced sample
// (e.g. a garbage collection on the heavy instance) means no move.
func TestRebalanceNeedsEverySample(t *testing.T) {
	heavy := steady(5, 1024*mb, 5)
	heavy[2] = healthSample{mem: 250 * mb, cpu: 5}
	d := run(map[string][]healthSample{
		"a": heavy,
		"b": steady(5, 200*mb, 5),
	})
	expect(t, d, nil, nil)
}

// CPU alone can trigger: 60% against 10%, memory even.
func TestRebalanceCpu(t *testing.T) {
	d := run(map[string][]healthSample{
		"a": steady(5, 500*mb, 60),
		"b": steady(5, 500*mb, 10),
		"c": steady(5, 480*mb, 12),
	})
	expect(t, d, []string{"a"}, []string{"b", "c"})
}

// A donor releases only the share by which it is above the average, so a
// small population does not swing from one side to the other.
func TestRebalanceShare(t *testing.T) {
	d := run(map[string][]healthSample{
		"a": steady(5, 600*mb, 5),
		"b": steady(5, 600*mb, 5),
		"c": steady(5, 60*mb, 5),
	})
	expect(t, d, []string{"a", "b"}, []string{"c"})
	// average 420MB: a is 30% above it, so 6 of its 20 conversations move.
	if got := d.share("a", 20, 50); got != 6 {
		t.Fatalf("share of 20 conversations: %d, expected 6", got)
	}
	// Capped by the batch.
	if got := d.share("a", 1000, 50); got != 50 {
		t.Fatalf("share of 1000 conversations: %d, expected the batch (50)", got)
	}
	// At least one, and none from an instance that owns none.
	if got := d.share("a", 1, 50); got != 1 {
		t.Fatalf("share of 1 conversation: %d, expected 1", got)
	}
	if got := d.share("a", 0, 50); got != 0 {
		t.Fatalf("share of 0 conversations: %d, expected 0", got)
	}
}

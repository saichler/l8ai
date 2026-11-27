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
	"sort"
	"strconv"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8bus/go/overlay/health"
	"github.com/saichler/l8types/go/ifs"
)

// healthSample is one chat instance's reported memory and CPU (its health).
type healthSample struct {
	mem uint64
	cpu float64
}

// Rebalancing (leader only, from the monitor goroutine): stress is relative
// to the other chat instances, never to a limit, so it works the same on any
// platform. Every RebalanceSampleEvery the leader samples each instance's
// health; every RebalanceEvery it checks the last RebalanceSamples samples:
//
//   - imbalance: in every one of them, on memory or on CPU, the highest
//     instance is at least RebalanceRatio x the lowest one and at least the
//     noise floor (RebalanceMemFloor / RebalanceCpuFloor) apart;
//   - donors are the instances above the average of an imbalanced metric,
//     receivers the ones below the average of every imbalanced metric;
//   - each donor releases up to RebalanceBatch conversations idle at least
//     RebalanceIdle, and they move to the least loaded receivers.
//
// Instances that are balanced, or apart by less than the noise floor, are left
// alone, however uneven their conversation counts are. Nothing moves while an
// instance is missing or a joined instance is not synced yet. After a move the
// samples restart, so the next decision sees the effect of this one.
func (this *Router) rebalanceTick(current map[string]bool) {
	now := time.Now()
	if now.Sub(this.lastSample) >= this.cfg.RebalanceSampleEvery {
		this.lastSample = now
		this.sampleHealth(current)
	}
	if now.Sub(this.lastRebalance) < this.cfg.RebalanceEvery {
		return
	}
	this.lastRebalance = now
	this.rebalance(current)
}

// sampleHealth records each chat instance's reported memory and CPU, keeping
// the last RebalanceSamples. An instance with no health report starts over.
func (this *Router) sampleHealth(current map[string]bool) {
	for id := range this.samples {
		if !current[id] {
			delete(this.samples, id)
		}
	}
	for id := range current {
		h := health.HealthOf(id, this.vnic.Resources())
		if h == nil || h.Stats == nil {
			delete(this.samples, id)
			continue
		}
		list := append(this.samples[id], healthSample{mem: h.Stats.MemoryUsage, cpu: h.Stats.CpuUsage})
		if len(list) > this.cfg.RebalanceSamples {
			list = list[len(list)-this.cfg.RebalanceSamples:]
		}
		this.samples[id] = list
	}
}

func (this *Router) rebalance(current map[string]bool) {
	log := this.vnic.Resources().Logger()
	ids := make([]string, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	n := this.cfg.RebalanceSamples
	if len(ids) < 2 || n < 1 || !this.synced || len(this.pendingSync) > 0 {
		return
	}
	this.mtx.Lock()
	unsettled := len(this.missingSince) + len(this.releasing)
	this.mtx.Unlock()
	if unsettled > 0 {
		return
	}
	for _, id := range ids {
		if len(this.samples[id]) < n {
			return // a new instance is not compared before it has n samples
		}
	}

	d := decide(ids, this.samples, this.cfg)
	if len(d.donors) == 0 || len(d.receivers) == 0 {
		return
	}
	donors, receivers := d.donors, d.receivers
	log.Info("router: rebalance: chat instances imbalanced (memory=", d.memImbalanced, ", cpu=", d.cpuImbalanced,
		") for ", n, " samples: ", describeLoad(ids, d.avgMem, d.avgCpu), "; donors ", donors, ", receivers ", receivers)

	// The donor waits one write-behind flush and a chat-orm write before it
	// answers, so its request gets more than a read's timeout.
	timeout := 2*this.cfg.ReadTimeoutSeconds + int(this.cfg.FlushInterval/time.Second) + 5
	ask := &l8ai.ChatRebalance{Count: int32(this.cfg.RebalanceBatch), IdleSeconds: int64(this.cfg.RebalanceIdle / time.Second)}
	for _, donor := range donors {
		resp := this.vnic.Request(donor, aicommon.ChatShCnvService, aicommon.ServiceArea, ifs.DELETE, ask, timeout)
		if resp == nil || resp.Error() != nil {
			reason := "no response"
			if resp != nil {
				reason = resp.Error().Error()
			}
			log.Warning("router: rebalance: instance ", donor, " did not release: ", reason)
			continue
		}
		rows := make([]*l8ai.ChatConvIndex, 0)
		for _, e := range resp.Elements() {
			if row, ok := e.(*l8ai.ChatConvIndex); ok && row != nil && row.ConversationId != "" {
				rows = append(rows, &l8ai.ChatConvIndex{ConversationId: row.ConversationId, OwnerUuid: donor})
			}
		}
		log.Info("router: rebalance: instance ", donor, " released ", len(rows), " idle conversations")
		this.reassignTo(rows, receivers)
	}
	this.samples = make(map[string][]healthSample)
}

// decision is the outcome of comparing the chat instances' last samples.
type decision struct {
	memImbalanced, cpuImbalanced bool
	donors, receivers            []string
	avgMem, avgCpu               map[string]float64
}

// decide compares the last cfg.RebalanceSamples samples of the instances (each
// has at least that many). See rebalanceTick for the rules.
func decide(ids []string, samples map[string][]healthSample, cfg *aicommon.ChatConfig) decision {
	n := cfg.RebalanceSamples
	d := decision{memImbalanced: true, cpuImbalanced: true}
	for i := 0; i < n; i++ {
		first := samples[ids[0]][len(samples[ids[0]])-n+i]
		minMem, maxMem, minCpu, maxCpu := first.mem, first.mem, first.cpu, first.cpu
		for _, id := range ids[1:] {
			s := samples[id][len(samples[id])-n+i]
			if s.mem < minMem {
				minMem = s.mem
			}
			if s.mem > maxMem {
				maxMem = s.mem
			}
			if s.cpu < minCpu {
				minCpu = s.cpu
			}
			if s.cpu > maxCpu {
				maxCpu = s.cpu
			}
		}
		if maxMem-minMem < cfg.RebalanceMemFloor || float64(maxMem) < cfg.RebalanceRatio*float64(minMem) {
			d.memImbalanced = false
		}
		if maxCpu-minCpu < cfg.RebalanceCpuFloor || maxCpu < cfg.RebalanceRatio*minCpu {
			d.cpuImbalanced = false
		}
	}
	if !d.memImbalanced && !d.cpuImbalanced {
		return d
	}
	d.avgMem = make(map[string]float64, len(ids))
	d.avgCpu = make(map[string]float64, len(ids))
	var allMem, allCpu float64
	for _, id := range ids {
		for _, s := range samples[id][len(samples[id])-n:] {
			d.avgMem[id] += float64(s.mem) / float64(n)
			d.avgCpu[id] += s.cpu / float64(n)
		}
		allMem += d.avgMem[id] / float64(len(ids))
		allCpu += d.avgCpu[id] / float64(len(ids))
	}
	for _, id := range ids {
		above := (d.memImbalanced && d.avgMem[id] > allMem) || (d.cpuImbalanced && d.avgCpu[id] > allCpu)
		below := (!d.memImbalanced || d.avgMem[id] < allMem) && (!d.cpuImbalanced || d.avgCpu[id] < allCpu)
		switch {
		case above:
			d.donors = append(d.donors, id)
		case below:
			d.receivers = append(d.receivers, id)
		}
	}
	return d
}

func describeLoad(ids []string, avgMem, avgCpu map[string]float64) string {
	result := ""
	for i, id := range ids {
		if i > 0 {
			result += ", "
		}
		result += id + " " + strconv.FormatFloat(avgMem[id]/(1<<20), 'f', 0, 64) + "MB/" +
			strconv.FormatFloat(avgCpu[id], 'f', 1, 64) + "%cpu"
	}
	return result
}

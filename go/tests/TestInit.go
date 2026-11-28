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

// Package tests holds every l8ai test (TestLocationAndApproach): end to end
// through the system API (vnic, HTTP) on the l8test topology, never calling an
// unexported function. External dependencies are stubs the tests start.
package tests

import (
	"github.com/saichler/l8bus/go/overlay/protocol"
	. "github.com/saichler/l8test/go/infra/t_resources"
	. "github.com/saichler/l8test/go/infra/t_topology"
	. "github.com/saichler/l8types/go/ifs"
)

var topo *TestTopology

func init() {
	Log.SetLogLevel(Info_Level)
}

func setup() {
	protocol.MessageLog = true
	topo = NewTestTopology(4, []int{20000, 30000, 40000}, Info_Level)
}

func tear() {
	topo.Shutdown()
}

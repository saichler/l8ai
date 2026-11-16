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

package common

import (
	"github.com/saichler/l8bus/go/overlay/health"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8health"
)

// Participants returns the live nodes that run an l8ai service, cluster-wide.
// It reads the health records, which every node holds: the service manager's
// GetParticipants only knows the participants of services the asking node
// hosts itself, so a router cannot use it to find chat instances.
func Participants(res ifs.IResources, serviceName string) map[string]bool {
	result := make(map[string]bool)
	for uuid, hp := range health.All(res) {
		if hp == nil || hp.Status != l8health.L8HealthState_Up || hp.Services == nil || hp.Services.ServiceToAreas == nil {
			continue
		}
		areas, ok := hp.Services.ServiceToAreas[serviceName]
		if ok && areas != nil && areas.Areas[int32(ServiceArea)] {
			result[uuid] = true
		}
	}
	return result
}

// IsParticipant reports whether a node runs an l8ai service.
func IsParticipant(res ifs.IResources, serviceName, uuid string) bool {
	return uuid != "" && Participants(res, serviceName)[uuid]
}

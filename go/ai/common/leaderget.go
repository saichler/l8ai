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
	"errors"

	"github.com/saichler/l8types/go/ifs"
)

// LeaderGet sends a read to the leader of an l8ai service (e.g. a chat-orm
// service) and returns the non-nil elements of the response.
func LeaderGet(vnic ifs.IVNic, serviceName string, query interface{}, timeoutSeconds int) ([]interface{}, error) {
	resp := vnic.LeaderRequest(serviceName, ServiceArea, ifs.GET, query, timeoutSeconds)
	if resp == nil {
		return nil, errors.New("no response from " + serviceName)
	}
	if resp.Error() != nil {
		return nil, resp.Error()
	}
	result := make([]interface{}, 0)
	for _, e := range resp.Elements() {
		if e != nil {
			result = append(result, e)
		}
	}
	return result, nil
}

// IsLeader reports whether this node leads the service (or its service group).
func IsLeader(vnic ifs.IVNic, serviceName string) bool {
	return vnic.Resources().Services().GetLeader(serviceName, ServiceArea) == vnic.Resources().SysConfig().LocalUuid
}

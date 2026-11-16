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

package orm

import (
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	l8c "github.com/saichler/l8common/go/common"
	"github.com/saichler/l8types/go/ifs"
)

// ChatStatDb persists user-chosen custom status only; online/away/offline are
// derived live by chat-users and never persisted.
func activateChatStatDb(creds, dbname string, vnic ifs.IVNic) {
	sla := l8c.NewOrmSLA(aicommon.ChatStatDbService, aicommon.ServiceArea, "UserId", newChatStatDbCallback(),
		&l8ai.ChatUserStatus{}, &l8ai.ChatUserStatusList{})
	sla.SetReplicationCount(1)
	l8c.ActivateService(sla, creds, dbname, vnic)
}

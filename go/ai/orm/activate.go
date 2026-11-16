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

// Package orm activates the chat persistence services. They run in exactly one
// process (chat-orm), with replication count 1 (plan §4.5, N1).
package orm

import (
	"github.com/saichler/l8types/go/ifs"
)

// ActivateOrm activates the four chat ORM services against one database.
func ActivateOrm(creds, dbname string, vnic ifs.IVNic) {
	activateChatMsgDb(creds, dbname, vnic)
	activateChatCnvDb(creds, dbname, vnic)
	activateChatGrpDb(creds, dbname, vnic)
	activateChatStatDb(creds, dbname, vnic)
}

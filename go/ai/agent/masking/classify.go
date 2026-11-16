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

// Package masking keeps sensitive values away from the LLM (plan §7.6).
// Values are replaced by deterministic tokens, [NAME_<6 base32 of
// HMAC(secret, conversationId|value)>], so the same value has the same token
// in a conversation across turns and agent instances. Each call builds its
// own unmask map; nothing is shared or persisted.
package masking

import "strings"

// Classification levels for field sensitivity.
const (
	ClassAlwaysMask = "always" // SSN, tax IDs, bank accounts: replaced by [MASKED], never restored
	ClassMaskName   = "name"   // names, addresses, emails, phones: [NAME_x]
	ClassMaskMoney  = "money"  // salaries, amounts, prices: [MONEY_x]
	ClassNeverMask  = "never"  // IDs, enums, statuses, dates, booleans, codes
)

var alwaysMaskPatterns = []string{"ssn", "nationalid", "bankaccount", "routingnumber", "taxid", "ein", "socialsecurity"}

var moneyMaskPatterns = []string{"salary", "amount", "price", "cost", "payment", "balance", "revenue", "income",
	"wage", "compensation"}

var nameMaskPatterns = []string{"firstname", "lastname", "name", "address", "email", "phone", "street", "city",
	"zip", "zipcode"}

// OverrideFunc lets an adopter override the classification of a field of a
// model. It returns nil to keep the default.
type OverrideFunc func(modelName string, fieldName string) *string

// Classifier classifies fields by model and field name.
type Classifier struct {
	overrides OverrideFunc
}

func NewClassifier(overrides OverrideFunc) *Classifier {
	return &Classifier{overrides: overrides}
}

// Classify returns the class of a field. The model name is the real model
// type resolved by the catalog, so adopter overrides can target it.
func (this *Classifier) Classify(modelName, fieldName string) string {
	if this.overrides != nil {
		if override := this.overrides(modelName, fieldName); override != nil {
			return *override
		}
	}
	lower := strings.ToLower(fieldName)
	for _, pattern := range alwaysMaskPatterns {
		if strings.Contains(lower, pattern) {
			return ClassAlwaysMask
		}
	}
	for _, pattern := range moneyMaskPatterns {
		if strings.Contains(lower, pattern) {
			return ClassMaskMoney
		}
	}
	for _, pattern := range nameMaskPatterns {
		if strings.Contains(lower, pattern) {
			return ClassMaskName
		}
	}
	return ClassNeverMask
}

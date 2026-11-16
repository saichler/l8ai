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

package masking

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

const masked = "[MASKED]"

var (
	ssnRegex   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	moneyRegex = regexp.MustCompile(`\$[\d,]+(?:\.\d{2})?`)
	emailRegex = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	phoneRegex = regexp.MustCompile(`\(?\d{3}\)?[\s\-]?\d{3}[\s\-]?\d{4}`)
	tokenRegex = regexp.MustCompile(`\[(NAME|MONEY)_[A-Z2-7]{6}\]`)
)

// Masker masks the text of one LLM call (or one turn) of a conversation and
// restores the tokens in the answer.
type Masker struct {
	cls            *Classifier
	secret         []byte
	conversationId string

	mtx    sync.Mutex
	values map[string]*maskedValue // token → value
}

type maskedValue struct {
	field string
	value interface{}
}

func NewMasker(cls *Classifier, secret []byte, conversationId string) *Masker {
	return &Masker{cls: cls, secret: secret, conversationId: conversationId, values: make(map[string]*maskedValue)}
}

// token returns the deterministic token of a value and records it for unmasking.
func (this *Masker) token(prefix, field string, value interface{}) string {
	mac := hmac.New(sha256.New, this.secret)
	mac.Write([]byte(this.conversationId + "|" + canonical(value)))
	sum := base32.StdEncoding.EncodeToString(mac.Sum(nil))
	token := "[" + prefix + "_" + sum[:6] + "]"
	this.mtx.Lock()
	this.values[token] = &maskedValue{field: field, value: value}
	this.mtx.Unlock()
	return token
}

// MaskText masks the obvious sensitive patterns of free text (user and
// assistant messages, summaries): SSNs, money amounts, emails and phones.
func (this *Masker) MaskText(text string) string {
	text = ssnRegex.ReplaceAllString(text, masked)
	text = moneyRegex.ReplaceAllStringFunc(text, func(m string) string { return this.token("MONEY", "", m) })
	text = emailRegex.ReplaceAllStringFunc(text, func(m string) string { return this.token("NAME", "email", m) })
	return phoneRegex.ReplaceAllStringFunc(text, func(m string) string { return this.token("NAME", "phone", m) })
}

// MaskJSON masks a tool result by field classification of its model.
func (this *Masker) MaskJSON(jsonText, model string) string {
	var data interface{}
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		return this.MaskText(jsonText)
	}
	out, err := json.Marshal(this.maskValue(data, model))
	if err != nil {
		return this.MaskText(jsonText)
	}
	return string(out)
}

func (this *Masker) maskValue(data interface{}, model string) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(v))
		for key, val := range v {
			switch this.cls.Classify(model, key) {
			case ClassAlwaysMask:
				result[key] = masked
			case ClassMaskName:
				result[key] = this.maskScalar("NAME", key, val, model)
			case ClassMaskMoney:
				result[key] = this.maskScalar("MONEY", key, val, model)
			default:
				result[key] = this.maskValue(val, model)
			}
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = this.maskValue(item, model)
		}
		return result
	}
	return data
}

func (this *Masker) maskScalar(prefix, field string, val interface{}, model string) interface{} {
	if val == nil {
		return nil
	}
	if list, ok := val.([]interface{}); ok {
		result := make([]interface{}, len(list))
		for i, item := range list {
			result[i] = this.maskScalar(prefix, field, item, model)
		}
		return result
	}
	return this.token(prefix, field, val)
}

// Unmask restores every token in the model's answer for display. Money is
// shown from cents with its currency (a Money object, or a scalar *amount
// field such as an aggregate of .amount).
func (this *Masker) Unmask(text string) string {
	return this.restore(text, true)
}

// UnmaskRaw restores the raw values, for the model's tool arguments.
func (this *Masker) UnmaskRaw(text string) string {
	return this.restore(text, false)
}

func (this *Masker) restore(text string, display bool) string {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	return tokenRegex.ReplaceAllStringFunc(text, func(token string) string {
		v, ok := this.values[token]
		if !ok {
			return token
		}
		if display && strings.HasPrefix(token, "[MONEY_") {
			return formatMoney(v.field, v.value)
		}
		return plain(v.value)
	})
}

func canonical(value interface{}) string {
	if s, ok := value.(string); ok {
		return s
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func plain(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
	}
	return canonical(value)
}

// formatMoney shows a Money object ({amount (cents), currencyCode}), or a
// scalar of an amount field (cents), as a currency amount. Text that was
// masked as written ("$1,200") is restored unchanged.
func formatMoney(field string, value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]interface{}:
		cents, ok := number(v["amount"])
		if !ok {
			return canonical(value)
		}
		code, _ := v["currencyCode"].(string)
		return withCurrency(cents/100, code)
	}
	n, ok := number(value)
	if !ok {
		return canonical(value)
	}
	if strings.HasSuffix(strings.ToLower(field), "amount") {
		return withCurrency(n/100, "")
	}
	return grouped(n)
}

func number(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		var f float64
		_, err := fmt.Sscanf(n, "%g", &f)
		return f, err == nil
	}
	return 0, false
}

func withCurrency(amount float64, code string) string {
	if code == "" || strings.EqualFold(code, "USD") {
		if amount < 0 {
			return "-$" + grouped(-amount)
		}
		return "$" + grouped(amount)
	}
	return grouped(amount) + " " + strings.ToUpper(code)
}

// grouped formats a number with two decimals and thousands separators.
func grouped(n float64) string {
	raw := fmt.Sprintf("%.2f", n)
	sign := ""
	if strings.HasPrefix(raw, "-") {
		sign, raw = "-", raw[1:]
	}
	parts := strings.SplitN(raw, ".", 2)
	digits := parts[0]
	buff := strings.Builder{}
	for i, ch := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			buff.WriteByte(',')
		}
		buff.WriteRune(ch)
	}
	return sign + buff.String() + "." + parts[1]
}

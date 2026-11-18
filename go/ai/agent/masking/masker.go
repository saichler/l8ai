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
//
// What is sensitive is declared in the models, never guessed: a field of a
// .proto message annotated [(l8ai.AIMask) = true] (aimask.proto) is masked,
// any other field is not. The annotation is read from the generated bindings'
// descriptors at run time.
//
// A masked value is replaced by a deterministic token, [MASK_<6 base32 of
// HMAC(secret, conversationId|value)>], so the same value has the same token
// in a conversation across turns and agent instances, and restored in the
// answer shown to the user. Each call builds its own unmask map; nothing is
// shared or persisted.
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

	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

var (
	// Free text (user messages, summaries) has no fields to read an
	// annotation from: the user's own SSNs, amounts, emails and phones are
	// masked by their shape.
	ssnRegex   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	moneyRegex = regexp.MustCompile(`\$[\d,]+(?:\.\d{2})?`)
	emailRegex = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	phoneRegex = regexp.MustCompile(`\(?\d{3}\)?[\s\-]?\d{3}[\s\-]?\d{4}`)

	tokenRegex       = regexp.MustCompile(`\[(MASK|NAME|MONEY)_[A-Z2-7]{6}\]`)
	quotedTokenRegex = regexp.MustCompile(`"\[(MASK|NAME|MONEY)_[A-Z2-7]{6}\]"`)
	// An aggregate of a query result (KeyCount): "sum(salary.amount)".
	aggregateRegex = regexp.MustCompile(`^\s*[A-Za-z_]+\s*\(\s*([A-Za-z0-9_.]+)\s*\)\s*$`)
)

// Resolver returns the descriptor of a model by its type name, or nil.
type Resolver func(model string) protoreflect.MessageDescriptor

// RegistryResolver resolves models through the registry of the agent, where
// the adopter registered its types.
func RegistryResolver(registry ifs.IRegistry) Resolver {
	return func(model string) protoreflect.MessageDescriptor {
		if registry == nil || model == "" {
			return nil
		}
		info, err := registry.Info(model)
		if err != nil || info == nil {
			return nil
		}
		instance, err := info.NewInstance()
		if err != nil {
			return nil
		}
		msg, ok := instance.(proto.Message)
		if !ok {
			return nil
		}
		return msg.ProtoReflect().Descriptor()
	}
}

// IsMasked reports whether a field is annotated [(l8ai.AIMask) = true].
func IsMasked(fd protoreflect.FieldDescriptor) bool {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return false
	}
	masked, _ := proto.GetExtension(opts, l8ai.E_AIMask).(bool)
	return masked
}

// Masker masks the text of one LLM call (or one turn) of a conversation and
// restores the tokens in the answer.
type Masker struct {
	resolve        Resolver
	secret         []byte
	conversationId string

	mtx    sync.Mutex
	values map[string]interface{} // token → value
}

func NewMasker(resolve Resolver, secret []byte, conversationId string) *Masker {
	return &Masker{resolve: resolve, secret: secret, conversationId: conversationId,
		values: make(map[string]interface{})}
}

// token returns the deterministic token of a value and records it for unmasking.
func (this *Masker) token(prefix string, value interface{}) string {
	mac := hmac.New(sha256.New, this.secret)
	mac.Write([]byte(this.conversationId + "|" + canonical(value)))
	sum := base32.StdEncoding.EncodeToString(mac.Sum(nil))
	token := "[" + prefix + "_" + sum[:6] + "]"
	this.mtx.Lock()
	this.values[token] = value
	this.mtx.Unlock()
	return token
}

// MaskText masks the obvious sensitive patterns of free text (user and
// assistant messages, summaries): SSNs, money amounts, emails and phones.
func (this *Masker) MaskText(text string) string {
	text = ssnRegex.ReplaceAllStringFunc(text, func(m string) string { return this.token("MASK", m) })
	text = moneyRegex.ReplaceAllStringFunc(text, func(m string) string { return this.token("MONEY", m) })
	text = emailRegex.ReplaceAllStringFunc(text, func(m string) string { return this.token("NAME", m) })
	return phoneRegex.ReplaceAllStringFunc(text, func(m string) string { return this.token("NAME", m) })
}

// MaskJSON masks the AIMask fields of a tool result about model (a record, a
// list of records or aggregates). A result of an unknown model has no
// annotations to apply and is returned as is.
func (this *Masker) MaskJSON(jsonText, model string) string {
	var md protoreflect.MessageDescriptor
	if this.resolve != nil {
		md = this.resolve(model)
	}
	if md == nil {
		return jsonText
	}
	var data interface{}
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		return this.MaskText(jsonText)
	}
	out, err := json.Marshal(this.maskValue(data, md))
	if err != nil {
		return this.MaskText(jsonText)
	}
	return string(out)
}

// maskValue walks a JSON value of message md. Keys that are not fields of md
// (the list wrapper, metadata) are walked with md, so the records inside are
// still masked.
func (this *Masker) maskValue(data interface{}, md protoreflect.MessageDescriptor) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(v))
		for key, val := range v {
			fd := fieldOf(md, key)
			switch {
			case fd == nil:
				if isScalar(val) && pathMasked(md, aggregateField(key)) {
					result[key] = this.maskAll(val)
				} else {
					result[key] = this.maskValue(val, md)
				}
			case IsMasked(fd):
				result[key] = this.maskAll(val)
			case fd.IsMap():
				result[key] = this.maskMap(val, fd.MapValue().Message())
			case fd.Message() != nil:
				result[key] = this.maskValue(val, fd.Message())
			default:
				result[key] = val
			}
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = this.maskValue(item, md)
		}
		return result
	}
	return data
}

// maskMap walks the values of a map field whose values are messages.
func (this *Masker) maskMap(val interface{}, md protoreflect.MessageDescriptor) interface{} {
	m, ok := val.(map[string]interface{})
	if !ok || md == nil {
		return val
	}
	result := make(map[string]interface{}, len(m))
	for k, item := range m {
		result[k] = this.maskValue(item, md)
	}
	return result
}

// maskAll replaces a masked value: a list item by item (the LLM can still
// count them), anything else (a scalar, a map, a whole message) as one token.
func (this *Masker) maskAll(val interface{}) interface{} {
	if val == nil {
		return nil
	}
	if list, ok := val.([]interface{}); ok {
		result := make([]interface{}, len(list))
		for i, item := range list {
			result[i] = this.maskAll(item)
		}
		return result
	}
	return this.token("MASK", val)
}

// Unmask restores every token in the model's answer for display.
func (this *Masker) Unmask(text string) string {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	return tokenRegex.ReplaceAllStringFunc(text, func(token string) string {
		v, ok := this.values[token]
		if !ok {
			return token
		}
		return display(v)
	})
}

// UnmaskRaw restores the raw values, for the model's tool arguments: a quoted
// token of a number, an object or a list becomes that JSON value.
func (this *Masker) UnmaskRaw(text string) string {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	text = quotedTokenRegex.ReplaceAllStringFunc(text, func(quoted string) string {
		v, ok := this.values[quoted[1:len(quoted)-1]]
		if !ok {
			return quoted
		}
		if _, isString := v.(string); isString {
			data, _ := json.Marshal(v)
			return string(data)
		}
		return canonical(v)
	})
	return tokenRegex.ReplaceAllStringFunc(text, func(token string) string {
		v, ok := this.values[token]
		if !ok {
			return token
		}
		return plain(v)
	})
}

// fieldOf returns the field of md with this JSON (or proto) name.
func fieldOf(md protoreflect.MessageDescriptor, key string) protoreflect.FieldDescriptor {
	if md == nil {
		return nil
	}
	fields := md.Fields()
	if fd := fields.ByJSONName(key); fd != nil {
		return fd
	}
	return fields.ByName(protoreflect.Name(key))
}

// aggregateField returns the field path of an aggregate key ("sum(a.b)" →
// "a.b"), or the key itself.
func aggregateField(key string) string {
	if m := aggregateRegex.FindStringSubmatch(key); m != nil {
		return m[1]
	}
	return key
}

// pathMasked reports whether a field path of md ("salary.amount",
// "base_salary") goes through an AIMask field. Names are matched as the query
// language does: case-insensitively, with or without underscores.
func pathMasked(md protoreflect.MessageDescriptor, path string) bool {
	if path == "" || path == "*" {
		return false
	}
	for _, part := range strings.Split(path, ".") {
		if md == nil {
			return false
		}
		fd := looseField(md, part)
		if fd == nil {
			return false
		}
		if IsMasked(fd) {
			return true
		}
		md = fd.Message()
	}
	return false
}

func looseField(md protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	want := loose(name)
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if loose(string(fd.Name())) == want || loose(fd.JSONName()) == want {
			return fd
		}
	}
	return nil
}

func loose(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

func isScalar(v interface{}) bool {
	switch v.(type) {
	case map[string]interface{}, []interface{}, nil:
		return false
	}
	return true
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

// display shows a masked value to the user: a Money message
// ({amount (cents), currencyCode}) as a currency amount, anything else as is.
func display(value interface{}) string {
	if m, ok := value.(map[string]interface{}); ok {
		if cents, ok := number(m["amount"]); ok {
			code, _ := m["currencyCode"].(string)
			return withCurrency(cents/100, code)
		}
	}
	return plain(value)
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

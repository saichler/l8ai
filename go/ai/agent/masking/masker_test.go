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
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/saichler/l8ai/go/types/l8ai"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The test models, as a .proto with AIMask annotations would compile:
//
//	message Money    { int64 amount = 1; string currency_code = 2; }
//	message Info     { string hostname = 1; uint32 gpu_count = 2; string ssn = 3; }
//	message Contact  { string kind = 1; string value = 2 [(l8ai.AIMask) = true]; }
//	message Employee {
//	  string employee_id = 1;
//	  string national_id = 2 [(l8ai.AIMask) = true];
//	  Money salary = 3 [(l8ai.AIMask) = true];
//	  Info device_info = 4;                         // looks sensitive by name, is not annotated
//	  repeated string emails = 5 [(l8ai.AIMask) = true];
//	  map<string, Contact> contacts = 6;
//	  Money bonus = 7;
//	  int64 age = 8 [(l8ai.AIMask) = true];
//	}
//	message EmployeeList { repeated Employee list = 1; }
var testFile = buildTestFile()

func buildTestFile() protoreflect.FileDescriptor {
	masked := func() *descriptorpb.FieldOptions {
		o := &descriptorpb.FieldOptions{}
		proto.SetExtension(o, l8ai.E_AIMask, true)
		return o
	}
	field := func(name string, n int32, t descriptorpb.FieldDescriptorProto_Type, typeName string,
		label descriptorpb.FieldDescriptorProto_Label, opts *descriptorpb.FieldOptions) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(n),
			Type: t.Enum(), Label: label.Enum(), Options: opts}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		return f
	}
	const (
		opt = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
		rep = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		str = descriptorpb.FieldDescriptorProto_TYPE_STRING
		i64 = descriptorpb.FieldDescriptorProto_TYPE_INT64
		u32 = descriptorpb.FieldDescriptorProto_TYPE_UINT32
		msg = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	)
	fdp := &descriptorpb.FileDescriptorProto{
		Name: proto.String("masktest.proto"), Package: proto.String("masktest"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Money"), Field: []*descriptorpb.FieldDescriptorProto{
				field("amount", 1, i64, "", opt, nil), field("currency_code", 2, str, "", opt, nil)}},
			{Name: proto.String("Info"), Field: []*descriptorpb.FieldDescriptorProto{
				field("hostname", 1, str, "", opt, nil), field("gpu_count", 2, u32, "", opt, nil),
				field("ssn", 3, str, "", opt, nil)}},
			{Name: proto.String("Contact"), Field: []*descriptorpb.FieldDescriptorProto{
				field("kind", 1, str, "", opt, nil), field("value", 2, str, "", opt, masked())}},
			{Name: proto.String("Employee"), Field: []*descriptorpb.FieldDescriptorProto{
				field("employee_id", 1, str, "", opt, nil),
				field("national_id", 2, str, "", opt, masked()),
				field("salary", 3, msg, ".masktest.Money", opt, masked()),
				field("device_info", 4, msg, ".masktest.Info", opt, nil),
				field("emails", 5, str, "", rep, masked()),
				field("contacts", 6, msg, ".masktest.Employee.ContactsEntry", rep, nil),
				field("bonus", 7, msg, ".masktest.Money", opt, nil),
				field("age", 8, i64, "", opt, masked())},
				NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("ContactsEntry"),
					Field: []*descriptorpb.FieldDescriptorProto{
						field("key", 1, str, "", opt, nil), field("value", 2, msg, ".masktest.Contact", opt, nil)},
					Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}}}},
			{Name: proto.String("EmployeeList"), Field: []*descriptorpb.FieldDescriptorProto{
				field("list", 1, msg, ".masktest.Employee", rep, nil)}},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		panic(err)
	}
	return fd
}

func testMasker() *Masker {
	return NewMasker(func(model string) protoreflect.MessageDescriptor {
		return testFile.Messages().ByName(protoreflect.Name(model))
	}, []byte("secret"), "conv-1")
}

var tokenOnly = regexp.MustCompile(`^\[MASK_[A-Z2-7]{6}\]$`)

func decode(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

const employee = `{"employeeId":"E1","nationalId":"123-45-6789","salary":{"amount":"9500000","currencyCode":"USD"},` +
	`"deviceInfo":{"hostname":"gpu-01","gpuCount":8,"ssn":"not annotated"},"emails":["a@x.com","b@x.com"],` +
	`"contacts":{"home":{"kind":"phone","value":"555-123-4567"}},"bonus":{"amount":"100000"},"age":"41"}`

func TestOnlyAnnotatedFieldsAreMasked(t *testing.T) {
	m := testMasker()
	out := decode(t, m.MaskJSON(`{"list":[`+employee+`]}`, "EmployeeList"))
	e := out["list"].([]interface{})[0].(map[string]interface{})

	if e["employeeId"] != "E1" {
		t.Fatalf("employeeId was masked: %v", e["employeeId"])
	}
	for _, key := range []string{"nationalId", "salary", "age"} {
		if s, _ := e[key].(string); !tokenOnly.MatchString(s) {
			t.Fatalf("%s is annotated but not masked: %v", key, e[key])
		}
	}
	// Not annotated, whatever its name looks like: deviceInfo and its ssn stay.
	info := e["deviceInfo"].(map[string]interface{})
	if info["gpuCount"] != float64(8) || info["hostname"] != "gpu-01" || info["ssn"] != "not annotated" {
		t.Fatalf("an unannotated message was masked: %v", info)
	}
	if bonus := e["bonus"].(map[string]interface{}); bonus["amount"] != "100000" {
		t.Fatalf("an unannotated Money was masked: %v", bonus)
	}
	// A masked list keeps its items, each one a token.
	emails := e["emails"].([]interface{})
	if len(emails) != 2 || !tokenOnly.MatchString(emails[0].(string)) || emails[0] == emails[1] {
		t.Fatalf("emails: %v", emails)
	}
	// A map of messages is walked: the annotated field inside each value is masked.
	contact := e["contacts"].(map[string]interface{})["home"].(map[string]interface{})
	if contact["kind"] != "phone" || !tokenOnly.MatchString(contact["value"].(string)) {
		t.Fatalf("contacts: %v", contact)
	}
}

func TestMaskedValuesAreRestoredForTheUser(t *testing.T) {
	m := testMasker()
	e := decode(t, m.MaskJSON(employee, "Employee"))
	answer := "ID " + e["nationalId"].(string) + " earns " + e["salary"].(string) + ", age " + e["age"].(string)
	if got, want := m.Unmask(answer), "ID 123-45-6789 earns $95,000.00, age 41"; got != want {
		t.Fatalf("Unmask = %q, want %q", got, want)
	}
	// The same value gets the same token in the conversation.
	again := decode(t, testMasker().MaskJSON(employee, "Employee"))
	if again["nationalId"] != e["nationalId"] {
		t.Fatal("the token of a value is not stable in its conversation")
	}
}

func TestUnmaskRawRestoresJSONValues(t *testing.T) {
	m := testMasker()
	e := decode(t, m.MaskJSON(employee, "Employee"))
	args := `{"data":{"nationalId":"` + e["nationalId"].(string) + `","salary":"` + e["salary"].(string) +
		`","age":"` + e["age"].(string) + `"},"note":"for ` + e["nationalId"].(string) + `"}`
	got := decode(t, m.UnmaskRaw(args))
	data := got["data"].(map[string]interface{})
	if data["nationalId"] != "123-45-6789" || data["age"] != "41" || got["note"] != "for 123-45-6789" {
		t.Fatalf("UnmaskRaw = %v", got)
	}
	if salary, ok := data["salary"].(map[string]interface{}); !ok || salary["amount"] != "9500000" {
		t.Fatalf("a masked message is not restored as an object: %v", data["salary"])
	}
}

func TestAggregatesOfMaskedFieldsAreMasked(t *testing.T) {
	m := testMasker()
	out := decode(t, m.MaskJSON(`{"sum(salary.amount)":9500000.00,"avg(Age)":41.00,"count(*)":3.00,`+
		`"sum(deviceInfo.gpuCount)":8.00,"Total":3}`, "Employee"))
	for _, key := range []string{"sum(salary.amount)", "avg(Age)"} {
		if s, _ := out[key].(string); !tokenOnly.MatchString(s) {
			t.Fatalf("%s aggregates a masked field but was not masked: %v", key, out[key])
		}
	}
	if out["count(*)"] != float64(3) || out["sum(deviceInfo.gpuCount)"] != float64(8) || out["Total"] != float64(3) {
		t.Fatalf("an aggregate of an unmasked field was masked: %v", out)
	}
}

func TestUnknownModelIsNotMasked(t *testing.T) {
	m := testMasker()
	if got := m.MaskJSON(employee, "NoSuchModel"); got != employee {
		t.Fatalf("a result of an unknown model was changed: %s", got)
	}
	if got := NewMasker(nil, nil, "c").MaskJSON(employee, "Employee"); got != employee {
		t.Fatalf("a masker without a resolver changed the result: %s", got)
	}
}

func TestIsMaskedReadsTheAnnotation(t *testing.T) {
	e := testFile.Messages().ByName("Employee")
	if !IsMasked(e.Fields().ByName("national_id")) || IsMasked(e.Fields().ByName("employee_id")) {
		t.Fatal("IsMasked does not follow the AIMask option")
	}
	info := testFile.Messages().ByName("Info")
	if IsMasked(info.Fields().ByName("ssn")) {
		t.Fatal("a field named ssn without the annotation is masked")
	}
}

func TestMaskTextStillMasksFreeText(t *testing.T) {
	m := testMasker()
	text := m.MaskText("my SSN is 123-45-6789, mail me at a@x.com")
	if strings.Contains(text, "123-45-6789") || strings.Contains(text, "a@x.com") {
		t.Fatalf("MaskText = %q", text)
	}
	if got := m.Unmask(text); got != "my SSN is 123-45-6789, mail me at a@x.com" {
		t.Fatalf("Unmask(MaskText) = %q", got)
	}
}

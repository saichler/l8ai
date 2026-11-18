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

package executor

import (
	"encoding/csv"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// device mirrors the shape of Probler's NetworkDevice:
//
//	message Info   { string hostname = 1; string vendor = 2; uint32 gpu_count = 3; repeated string tags = 4; }
//	message Device { string id = 1; Info device_info = 2; repeated Info history = 3; map<string,string> labels = 4; int64 uptime = 5; }
func device() protoreflect.MessageDescriptor {
	opt, rep := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL, descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	str, msg := descriptorpb.FieldDescriptorProto_TYPE_STRING, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	f := func(name string, n int32, t descriptorpb.FieldDescriptorProto_Type, typeName string, l descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
		d := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(n), Type: t.Enum(), Label: l.Enum()}
		if typeName != "" {
			d.TypeName = proto.String(typeName)
		}
		return d
	}
	fdp := &descriptorpb.FileDescriptorProto{Name: proto.String("dev.proto"), Package: proto.String("dev"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Info"), Field: []*descriptorpb.FieldDescriptorProto{
				f("hostname", 1, str, "", opt), f("vendor", 2, str, "", opt),
				f("gpu_count", 3, descriptorpb.FieldDescriptorProto_TYPE_UINT32, "", opt), f("tags", 4, str, "", rep)}},
			{Name: proto.String("Device"), Field: []*descriptorpb.FieldDescriptorProto{
				f("id", 1, str, "", opt), f("device_info", 2, msg, ".dev.Info", opt), f("history", 3, msg, ".dev.Info", rep),
				f("labels", 4, msg, ".dev.Device.LabelsEntry", rep), f("uptime", 5, descriptorpb.FieldDescriptorProto_TYPE_INT64, "", opt)},
				NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("LabelsEntry"),
					Field:   []*descriptorpb.FieldDescriptorProto{f("key", 1, str, "", opt), f("value", 2, str, "", opt)},
					Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}}}},
		}}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		panic(err)
	}
	return fd.Messages().ByName("Device")
}

func TestDefaultColumnsFlattenOneLevelAndSkipListsAndMaps(t *testing.T) {
	got := defaultColumns(device())
	want := []string{"id", "deviceInfo.hostname", "deviceInfo.vendor", "deviceInfo.gpuCount", "uptime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaultColumns = %v, want %v", got, want)
	}
}

func TestWriteCSVEscapesAndFillsMissingValues(t *testing.T) {
	records := []map[string]interface{}{
		{"id": "10.20.30.1", "deviceInfo": map[string]interface{}{"hostname": "core-1", "vendor": "Cisco Systems, Inc.", "gpuCount": float64(0)}, "uptime": "393084300"},
		{"id": "10.20.30.4", "deviceInfo": map[string]interface{}{"vendor": `Palo "Alto"`}, "tags": []interface{}{"a", "b"}},
	}
	data, err := writeCSV([]string{"id", "deviceInfo.hostname", "deviceInfo.vendor", "deviceInfo.gpuCount", "uptime", "tags"}, records)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, data)
	}
	want := [][]string{
		{"id", "deviceInfo.hostname", "deviceInfo.vendor", "deviceInfo.gpuCount", "uptime", "tags"},
		{"10.20.30.1", "core-1", "Cisco Systems, Inc.", "0", "393084300", ""},
		{"10.20.30.4", "", `Palo "Alto"`, "", "", `["a","b"]`},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %q\nwant  %q", rows, want)
	}
}

func TestFileNamesAreSafeAndTyped(t *testing.T) {
	cases := map[string]string{
		"network_devices.csv":    "network_devices.csv",
		"../../etc/passwd":       "passwd.txt",
		"report":                 "report.txt",
		"my report: Q3?.md":      "my report_ Q3_.md",
		"":                       "file.txt",
		`C:\Users\x\devices.csv`: "devices.csv",
	}
	for in, want := range cases {
		if got := fileName(in, "file", ".txt"); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := fileName("", "NetworkDevice", ".csv"); got != "NetworkDevice.csv" {
		t.Errorf("fallback = %q", got)
	}
	for name, want := range map[string]string{"a.csv": "text/csv", "a.md": "text/markdown", "a.json": "application/json", "a.txt": "text/plain", "a.bin": "text/plain"} {
		if got := mimeOf(name); got != want {
			t.Errorf("mimeOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExportPagingClausesAreReplaced(t *testing.T) {
	q := "select * from NetworkDevice where deviceInfo.vendor=Cisco sort-by id limit 25 page 2"
	if m := limitClauseRe.FindStringSubmatch(q); m == nil || m[1] != "25" {
		t.Fatalf("the limit was not found: %v", m)
	}
	base := strings.TrimSpace(pageClauseRe.ReplaceAllString(limitClauseRe.ReplaceAllString(q, ""), ""))
	if base != "select * from NetworkDevice where deviceInfo.vendor=Cisco sort-by id" {
		t.Fatalf("base = %q", base)
	}
}

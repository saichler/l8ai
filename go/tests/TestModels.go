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

package tests

import (
	"github.com/saichler/l8ai/go/ai/agent/catalog"
	"github.com/saichler/l8ai/go/types/l8aitest"
	"github.com/saichler/l8services/go/services/base"
	"github.com/saichler/l8types/go/ifs"
)

// The application model the agent's tools use in the tests: TestEmployee
// (l8aitest), with (l8ai.AIMask) fields, served from memory by an l8services
// BaseService, as an application's service would serve it.
const (
	testEmpService = "TestEmp"
	testEmpArea    = byte(5)
)

// registerTestTypes registers the test model on a node, as an application's
// model registration does.
func registerTestTypes(r ifs.IResources) {
	r.Registry().Register(&l8aitest.TestEmployee{})
	r.Registry().Register(&l8aitest.TestEmployeeList{})
	r.Introspector().Decorators().AddPrimaryKeyDecorator(&l8aitest.TestEmployee{}, "EmployeeId")
}

// testServiceMap points the agent's catalog at the test model's service.
var testServiceMap = map[string]catalog.ServiceRef{"TestEmployee": {Service: testEmpService, Area: int32(testEmpArea)}}

// The seeded employees: e1 has every field, e2 has no device info, e3 no
// emails or contacts.
func testEmployees() []interface{} {
	usd := func(n int64) *l8aitest.TestMoney { return &l8aitest.TestMoney{Amount: n, CurrencyCode: "USD"} }
	return []interface{}{
		&l8aitest.TestEmployee{EmployeeId: "e1", Name: `Smith, "Jo"`, NationalId: "NID-111-22", Salary: usd(120000),
			DeviceInfo: &l8aitest.TestDeviceInfo{Hostname: "core-1", GpuCount: 4, Ssn: "078-05-1120"},
			Emails:     []string{"jo@corp.example"},
			Contacts:   map[string]*l8aitest.TestContact{"home": {Kind: "phone", Value: "+1-555-0100"}},
			Bonus:      usd(5000), Age: 41, Status: l8aitest.TestEmployeeStatus_TEST_EMPLOYEE_STATUS_ACTIVE,
			Tags: []string{"a", "b"}},
		&l8aitest.TestEmployee{EmployeeId: "e2", Name: "Lee", NationalId: "NID-333-44",
			Salary: &l8aitest.TestMoney{Amount: 90000, CurrencyCode: "EUR"}, Age: 29,
			Status: l8aitest.TestEmployeeStatus_TEST_EMPLOYEE_STATUS_ON_LEAVE},
		&l8aitest.TestEmployee{EmployeeId: "e3", Name: "Kim", NationalId: "NID-555-66", Salary: usd(70000),
			DeviceInfo: &l8aitest.TestDeviceInfo{Hostname: "edge-2"}, Age: 35},
	}
}

// activateTestEmployees serves the seeded employees on a node.
func activateTestEmployees(nic ifs.IVNic) {
	registerTestTypes(nic.Resources())
	sla := ifs.NewServiceLevelAgreement(&base.BaseService{}, testEmpService, testEmpArea, true, nil)
	sla.SetServiceItem(&l8aitest.TestEmployee{})
	sla.SetServiceItemList(&l8aitest.TestEmployeeList{})
	sla.SetPrimaryKeys("EmployeeId")
	sla.SetVoter(true)
	sla.SetTransactional(false)
	sla.SetInitItems(testEmployees())
	if _, err := base.Activate(sla, nic); err != nil {
		panic(err)
	}
}

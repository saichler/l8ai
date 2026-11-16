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

// Package catalog maps model types to the services that serve them, across
// the whole cluster (plan §7.3). It merges the health records of every node
// (health.ServiceCatalog) with the local services and the adopter's optional
// ServiceMap, and refreshes as services come and go.
package catalog

import (
	"sort"
	"strings"
	"sync"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8bus/go/overlay/health"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8reflect"
)

// Entry is one model and the service that serves it.
type Entry struct {
	Model      string
	Service    string
	Area       int32
	PrimaryKey string
}

// ServiceRef is an adopter-supplied service of a model (AgentConfig.ServiceMap, N6).
type ServiceRef struct {
	Service string
	Area    int32
}

// Catalog is the cluster-wide model → service map.
type Catalog struct {
	res        ifs.IResources
	serviceMap map[string]ServiceRef

	mtx         sync.RWMutex
	entries     map[string]*Entry // by lower-case model name
	descriptors map[string]*Entry // from the web descriptors, by lower-case model name
	version     int               // bumped when the entries change
}

func New(res ifs.IResources, serviceMap map[string]ServiceRef) *Catalog {
	c := &Catalog{res: res, serviceMap: serviceMap, entries: make(map[string]*Entry),
		descriptors: make(map[string]*Entry)}
	c.Refresh()
	return c
}

// addDescriptor records a model served by a service, from its web
// descriptor, and refreshes the entries.
func (this *Catalog) addDescriptor(model, service string, area int32) {
	key := strings.ToLower(model)
	this.mtx.Lock()
	_, known := this.descriptors[key]
	this.descriptors[key] = &Entry{Model: model, Service: service, Area: area}
	this.mtx.Unlock()
	if !known {
		this.res.Logger().Info("catalog: ", model, " is served by ", service, "/", area)
	}
	this.Refresh()
}

// Refresh rebuilds the entries: the services' web descriptors first, then the
// models the health records and local services name, then the adopter's
// ServiceMap over all. The chat services (area 81) are not offered to the model.
func (this *Catalog) Refresh() {
	entries := make(map[string]*Entry)
	add := func(service string, area int32, model string) {
		if model == "" || area == int32(aicommon.ServiceArea) {
			return
		}
		key := strings.ToLower(model)
		if _, ok := entries[key]; ok {
			return // the first source wins: descriptors, then health, then local
		}
		entries[key] = &Entry{Model: model, Service: service, Area: area, PrimaryKey: this.primaryKey(model)}
	}
	// Web descriptors name exactly what the UI reaches; only models this node
	// has registered (CallRegisterModels) can be described and decoded.
	this.mtx.RLock()
	described := make([]Entry, 0, len(this.descriptors))
	for _, e := range this.descriptors {
		described = append(described, *e)
	}
	this.mtx.RUnlock()
	for _, e := range described {
		if _, ok := this.res.Introspector().NodeByTypeName(e.Model); ok {
			add(e.Service, e.Area, e.Model)
		}
	}
	for service, areas := range health.ServiceCatalog(this.res) {
		if areas != nil {
			for area, model := range areas.Models {
				add(service, area, model)
			}
		}
	}
	if local := this.res.SysConfig().Services; local != nil {
		for service, areas := range local.ServiceToAreas {
			if areas != nil {
				for area, model := range areas.Models {
					add(service, area, model)
				}
			}
		}
	}
	for model, ref := range this.serviceMap {
		entries[strings.ToLower(model)] = &Entry{Model: model, Service: ref.Service, Area: ref.Area,
			PrimaryKey: this.primaryKey(model)}
	}
	this.mtx.Lock()
	defer this.mtx.Unlock()
	if !sameEntries(this.entries, entries) {
		this.entries = entries
		this.version++
	}
}

// Version changes whenever the entries change (the tool enum and Tier-1 text follow it).
func (this *Catalog) Version() int {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	return this.version
}

// Resolve returns the entry of a model name, case-insensitively.
func (this *Catalog) Resolve(model string) (*Entry, bool) {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	e, ok := this.entries[strings.ToLower(model)]
	if !ok {
		return nil, false
	}
	copy := *e
	return &copy, true
}

// Entries returns the entries allowed by the filter (nil allows all), sorted by model.
func (this *Catalog) Entries(allowed map[string]bool) []Entry {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	result := make([]Entry, 0, len(this.entries))
	for _, e := range this.entries {
		if allowed == nil || allowed[e.Model] {
			result = append(result, *e)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Model < result[j].Model })
	return result
}

func (this *Catalog) primaryKey(model string) string {
	node, ok := this.res.Introspector().NodeByTypeName(model)
	if !ok {
		return ""
	}
	fields, err := this.res.Introspector().Decorators().Fields(node, l8reflect.L8DecoratorType_Primary)
	if err != nil || len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func sameEntries(a, b map[string]*Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || *v != *w {
			return false
		}
	}
	return true
}

// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"fmt"

	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

// ResolveHerdInputs validates a mission's requested MCP endpoints (against what
// `principal` may consume) and lookbook ids, returning the endpoint names and the
// resolved design-guideline strings to inject. A returned error names the offending
// endpoint or lookbook id and is a client/validation error — callers map it to
// their transport (HTTP 400 / MCP tool error). Nil gw or ta skips that half.
func ResolveHerdInputs(gw *gateway.Store, ta *taskartifacts.Store, principal string, endpoints []string, lookbookIDs []int64) (endpointNames []string, guidelines []string, err error) {
	if len(endpoints) > 0 && gw != nil {
		usable, _ := gw.Usable(principal)
		ok := map[string]bool{}
		for _, e := range usable {
			ok[e.Name] = true
		}
		for _, name := range endpoints {
			if !ok[name] {
				return nil, nil, fmt.Errorf("unknown or inaccessible MCP endpoint %q", name)
			}
			endpointNames = append(endpointNames, name)
		}
	}
	if len(lookbookIDs) > 0 && ta != nil {
		for _, id := range lookbookIDs {
			item, ierr := ta.GetLookbookItem(id)
			if ierr != nil || item == nil {
				return nil, nil, fmt.Errorf("unknown lookbook item %d", id)
			}
			guidelines = append(guidelines, item.Name+": "+item.Description)
		}
	}
	return endpointNames, guidelines, nil
}

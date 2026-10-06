package ny

import (
	"encoding/json"
	"sort"
	"strconv"
)

const defaultMaxJSONStructureEntries = 1024

// Paths use typed, length-prefixed segments so object keys cannot impersonate
// array positions or collide with other object-key sequences.
func inspectJSONStructure(document any, maxEntries int) JSONStructureReport {
	if maxEntries <= 0 {
		maxEntries = defaultMaxJSONStructureEntries
	}
	report := JSONStructureReport{Entries: []JSONStructureEntry{}}
	indices := make(map[string]int)
	var visit func(any, string)
	visit = func(value any, path string) {
		kind := jsonValueKind(value)
		identity := path + "\x00" + kind
		if index, ok := indices[identity]; ok {
			report.Entries[index].Occurrences++
		} else if len(report.Entries) < maxEntries {
			indices[identity] = len(report.Entries)
			report.Entries = append(report.Entries, JSONStructureEntry{
				PathSHA256:  digestString("json-structure-path/v1\x00" + path),
				Kind:        kind,
				Occurrences: 1,
			})
		} else {
			report.Truncated = true
		}
		switch typed := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				visit(typed[key], path+"\x00k"+strconv.Itoa(len(key))+":"+key)
			}
		case []any:
			childPath := path + "\x00a"
			for _, child := range typed {
				visit(child, childPath)
			}
		}
	}
	visit(document, "$")
	sort.Slice(report.Entries, func(left, right int) bool {
		a, b := report.Entries[left], report.Entries[right]
		if a.PathSHA256 != b.PathSHA256 {
			return a.PathSHA256 < b.PathSHA256
		}
		return a.Kind < b.Kind
	})
	return report
}

func jsonValueKind(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	default:
		return "null"
	}
}

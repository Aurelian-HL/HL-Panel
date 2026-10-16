package importedconfig

import (
	"bytes"
	"encoding/json"
	"io"
)

// Reject duplicate object keys instead of allowing ambiguous last-value wins.
// Nested strings containing native JSON are checked again by object().
func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				t, e := d.Token()
				if e != nil {
					return false
				}
				k, ok := t.(string)
				if !ok || seen[k] {
					return false
				}
				seen[k] = true
				if !walk(depth + 1) {
					return false
				}
			}
			t, e := d.Token()
			return e == nil && t == json.Delim('}')
		case '[':
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			t, e := d.Token()
			return e == nil && t == json.Delim(']')
		default:
			return false
		}
	}
	if !walk(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func equalMaps(a, b map[string]json.RawMessage) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return equalJSON(x, y)
}

func validateOutbound(o map[string]json.RawMessage) error {
	switch text(o["protocol"]) {
	case "freedom":
		if len(o["settings"]) > 0 {
			s, err := object(o["settings"])
			if err != nil {
				return err
			}
			if text(s["redirect"]) != "" {
				return invalid("直连出口包含固定目标")
			}
		}
	case "socks":
		s, err := object(o["settings"])
		if err != nil {
			return err
		}
		servers, err := list[map[string]json.RawMessage](s["servers"])
		if err != nil || len(servers) != 1 {
			return invalid("SOCKS出口结构暂不支持")
		}
		v := servers[0]
		port := number(v["port"])
		if text(v["address"]) == "" || port < 1 || port > 65535 {
			return invalid("SOCKS出口地址无效")
		}
		if len(v["users"]) > 0 {
			users, err := list[map[string]json.RawMessage](v["users"])
			if err != nil || len(users) != 1 || text(users[0]["user"]) == "" || text(users[0]["pass"]) == "" {
				return invalid("SOCKS出口认证无效")
			}
		}
	default:
		return invalid("线路出口暂不支持")
	}
	return nil
}

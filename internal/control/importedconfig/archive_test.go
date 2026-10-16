package importedconfig

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/importedconfig/testfixture"
)

func TestArchivePreservesNativeParametersAndRedactsViews(t *testing.T) {
	raw := testfixture.Raw()
	plan, err := Validate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lines) != 1 || !plan.Lines[0].EmptyFlow || !plan.Lines[0].MLDSA || plan.Lines[0].ShortIDCount != 2 || plan.Lines[0].ServerNameCount != 2 {
		t.Fatal("native compatibility fields lost")
	}
	view, _ := json.Marshal(plan)
	for _, secret := range []string{"11111111-1111-4111-8111-111111111111", "SYNTHETIC-SECRET-TOKEN", "privateKey", "mldsa65Seed"} {
		if bytes.Contains(view, []byte(secret)) {
			t.Fatal("secret in public view")
		}
	}

}

func TestArchiveRejectsAmbiguityAndCredentialMismatch(t *testing.T) {
	tests := map[string]func(map[string]any){
		"changed-uuid": func(d map[string]any) {
			i := d["config"].(map[string]any)["inbounds"].([]any)[0].(map[string]any)
			i["settings"] = map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": "22222222-2222-4222-8222-222222222222", "flow": ""}}}
		},
		"ambiguous-routing": func(d map[string]any) {
			d["config"].(map[string]any)["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"inbound-25510"}, "outboundTag": "direct"}, map[string]any{"type": "field", "inboundTag": []string{"inbound-25510"}, "outboundTag": "direct"}}}
		},
		"redirect": func(d map[string]any) {
			d["config"].(map[string]any)["outbounds"].([]any)[0].(map[string]any)["settings"] = map[string]any{"redirect": "example.org:443"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			d := testfixture.Document()
			mutate(d)
			b, _ := json.Marshal(d)
			if _, err := Validate(b); err == nil {
				t.Fatal("accepted invalid archive")
			}
		})
	}
	raw := strings.Replace(string(testfixture.Raw()), `"host":"source.example.invalid"`, `"host":"source.example.invalid","host":"source.example.invalid"`, 1)
	if _, err := Validate([]byte(raw)); err == nil {
		t.Fatal("accepted duplicate key")
	}
	if _, err := Validate(append(testfixture.Raw(), []byte("{}")...)); err == nil {
		t.Fatal("accepted trailing document")
	}
}

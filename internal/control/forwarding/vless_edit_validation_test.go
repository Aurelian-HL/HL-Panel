package forwarding

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestVLESSCredentialOmissionIsOnlyAcceptedForUpdates(t *testing.T) {
	for _, test := range []struct {
		name     string
		revision int64
		username string
		password string
		valid    bool
	}{
		{name: "new rule requires credentials"},
		{name: "existing rule retains both", revision: 1, valid: true},
		{name: "legacy update username is checked by repository", revision: 1, username: "landing-user", valid: true},
		{name: "password alone is invalid", revision: 1, password: "landing-secret"},
		{name: "complete replacement is valid", revision: 1, username: "landing-user", password: "landing-secret", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest()
			request.IngressProtocol = IngressVLESSReality
			request.Targets = nil
			request.VLESSSOCKS5Host = "landing.example.test"
			request.VLESSSOCKS5Port = 1080
			request.VLESSSOCKS5Username = test.username
			request.VLESSSOCKS5Password = test.password
			request.Revision = test.revision
			_, err := NormalizeRequest(request)
			if test.valid && err != nil || !test.valid && !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("credential validation result: %v", err)
			}
			if test.revision > 0 && test.username == "" && test.password == "" {
				repo := &captureRepository{}
				if _, _, err := NewService(repo, nil).Create(context.Background(), "admin", request, "create-with-revision"); !errors.Is(err, faults.ErrValidation) || repo.calls != 0 {
					t.Fatal("creation bypassed credential requirements using a positive revision")
				}
			}
		})
	}
}

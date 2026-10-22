package customers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestLegacyProvisioningDoesNotGrantPanelLoginAndKeepsFingerprint(t *testing.T) {
	for _, password := range []string{"", "generated-integration-password"} {
		t.Run(map[bool]string{true: "with-password", false: "without-password"}[password != ""], func(t *testing.T) {
			repo := &recordingRepository{}
			input := validCustomerInput()
			input.Username = strings.ToLower(input.Username)
			input.AccountKind, input.Password = "", password
			legacyHash, err := customerRequestFingerprint("admin", "", input)
			if err != nil {
				t.Fatal(err)
			}
			item, _, err := NewService(repo, nil).CreateCustomer(context.Background(), "admin", input)
			if err != nil {
				t.Fatal(err)
			}
			write := repo.customerWrites[0]
			if item.AccountKind != AccountService || len(write.Customer.PasswordHash) != 0 || write.PasswordChanged || write.RequestSHA256 != legacyHash {
				t.Fatal("legacy provisioning created login material or changed retry fingerprint")
			}
		})
	}
}

func TestLegacyServiceUpdateIgnoresLoginPasswordAndPreservesFingerprint(t *testing.T) {
	repo := &recordingRepository{item: Customer{ID: "service-owner", AccountKind: AccountService, Revision: 1}}
	input := validCustomerInput()
	input.Username = strings.ToLower(input.Username)
	input.AccountKind, input.Revision = "", 1
	legacyHash, err := customerRequestFingerprint("admin", repo.item.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := NewService(repo, nil).UpdateCustomer(context.Background(), "admin", repo.item.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	write := repo.customerWrites[0]
	if item.AccountKind != AccountService || len(write.Customer.PasswordHash) != 0 || write.PasswordChanged || write.RequestSHA256 != legacyHash {
		t.Fatal("service update created login material or changed retry fingerprint")
	}
	input.AccountKind = AccountPanel
	if _, _, err := NewService(repo, nil).UpdateCustomer(context.Background(), "admin", repo.item.ID, input); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("service owner converted to a panel login", err)
	}
	input.AccountKind = AccountService
	if _, _, err := NewService(repo, nil).UpdateCustomer(context.Background(), "admin", repo.item.ID, input); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("explicit service update accepted a password", err)
	}
}

func TestLegacyProvisioningRejectsInvalidPasswordWithoutWriting(t *testing.T) {
	for _, password := range []string{strings.Repeat("x", 1025), string([]byte{0xff})} {
		repo := &recordingRepository{}
		input := validCustomerInput()
		input.AccountKind, input.Password = "", password
		if _, _, err := NewService(repo, nil).CreateCustomer(context.Background(), "admin", input); !errors.Is(err, faults.ErrValidation) || len(repo.customerWrites) != 0 {
			t.Fatal("invalid legacy password accepted or persisted")
		}
	}
}

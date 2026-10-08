package main

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nezhamonitor"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/postgressnapshot"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

type controlRepository interface {
	customerIdentityRepositoryProvider
	audit.Repository
	auth.Repository
	enrollment.Repository
	nodes.Repository
	nezhamonitor.BindingSource
	groups.Repository
	endpoints.Repository
	generations.Repository
	customers.Repository
	forwarding.Repository
	gatewaymembership.Repository
	gatewaymembership.ProtocolProbeConfigSource
	gatewaymembership.ProtocolProbeConfigurator
	groupconfig.Repository
	rulegroups.Repository
	siteconfig.Repository
	subscriptions.Repository
	announcements.Repository
	vlessidentity.Repository
	vlessruntime.Repository
	deploymentreceipts.Repository
}

func openRepository(ctx context.Context, config runtimeConfig, bootstrap auth.Administrator) (controlRepository, func(), error) {
	if config.DatabaseURL != "" {
		store, err := postgressnapshot.Open(ctx, config.DatabaseURL, bootstrap)
		if err != nil {
			return nil, nil, err
		}
		return store, func() { _ = store.Close() }, nil
	}
	return memoryrepo.New(bootstrap), func() {}, nil
}

package panelruntime

import (
	"context"
	"errors"
	"os/exec"
)

// SystemdController is intentionally fixed to the installed control API unit.
// No service name or shell fragment ever comes from an HTTP request.
type SystemdController struct{ service string }

func NewSystemdController(service string) Controller {
	if service == "" {
		service = "hl-panel-control-api.service"
	}
	if service != "hl-panel-control-api.service" {
		return nil
	}
	return SystemdController{service: service}
}

func (c SystemdController) Stop(ctx context.Context) error    { return c.run(ctx, "stop") }
func (c SystemdController) Restart(ctx context.Context) error { return c.run(ctx, "restart") }

func (c SystemdController) run(ctx context.Context, action string) error {
	if c.service != "hl-panel-control-api.service" || (action != "stop" && action != "restart") {
		return errors.New("invalid systemd control request")
	}
	if err := exec.CommandContext(ctx, "systemctl", action, c.service).Run(); err != nil {
		return errors.New("systemd control failed")
	}
	return nil
}

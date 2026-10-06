// Package health contains local observations only. It never mutates desired
// configuration and never executes a remote command.
package health

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Status string

const (
	StatusUnknown  Status = "unknown"
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusFailed   Status = "failed"
)

type Observation struct {
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
}

type Snapshot struct {
	CheckedAt time.Time   `json:"checked_at"`
	Engine    Observation `json:"engine"`
	Egress    Observation `json:"egress"`
}

type EngineProbe interface {
	CheckEngine(context.Context) error
}

type EgressProbe interface {
	CheckEgress(context.Context) error
}

type Checker struct {
	engine EngineProbe
	egress EgressProbe
	now    func() time.Time
}

func NewChecker(engine EngineProbe, egress EgressProbe) *Checker {
	return &Checker{engine: engine, egress: egress, now: time.Now}
}

func (c *Checker) Check(ctx context.Context) Snapshot {
	snapshot := Snapshot{CheckedAt: c.now().UTC(), Engine: Observation{Status: StatusUnknown}, Egress: Observation{Status: StatusUnknown}}
	if c.engine == nil {
		snapshot.Engine = Observation{Status: StatusUnknown, Message: "engine probe is not configured"}
	} else if err := c.engine.CheckEngine(ctx); err != nil {
		snapshot.Engine = failedObservation(err)
	} else {
		snapshot.Engine = Observation{Status: StatusHealthy}
	}
	if c.egress == nil {
		snapshot.Egress = Observation{Status: StatusUnknown, Message: "egress probe is not configured"}
	} else if err := c.egress.CheckEgress(ctx); err != nil {
		snapshot.Egress = failedObservation(err)
	} else {
		snapshot.Egress = Observation{Status: StatusHealthy}
	}
	return snapshot
}

func failedObservation(err error) Observation {
	if err == nil {
		return Observation{Status: StatusFailed}
	}
	message := strings.Join(strings.Fields(strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, err.Error())), " ")
	message = redactSecrets(message)
	if len(message) > 512 {
		message = message[:512]
	}
	return Observation{Status: StatusFailed, Message: message}
}

func redactSecrets(message string) string {
	parts := strings.Fields(message)
	for index := 0; index < len(parts); index++ {
		lower := strings.ToLower(parts[index])
		if lower == "bearer" && index+1 < len(parts) {
			parts[index+1] = "[redacted]"
			index++
			continue
		}
		for _, prefix := range []string{"token=", "credential=", "password="} {
			if strings.HasPrefix(lower, prefix) {
				parts[index] = parts[index][:len(prefix)] + "[redacted]"
				break
			}
		}
	}
	return strings.Join(parts, " ")
}

type StaticEngineProbe struct {
	Err error
}

func (p StaticEngineProbe) CheckEngine(context.Context) error {
	return p.Err
}

type StaticEgressProbe struct {
	Err error
}

func (p StaticEgressProbe) CheckEgress(context.Context) error {
	return p.Err
}

var ErrUnavailable = errors.New("observation unavailable")

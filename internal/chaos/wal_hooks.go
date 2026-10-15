package chaos

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// InstallWALHooks wires injector into wal.Config.OnFault.
// Safe when inj is nil (no-op). Production leaves OnFault nil.
func InstallWALHooks(cfg *wal.Config, inj *Injector) {
	if cfg == nil || inj == nil {
		return
	}
	cfg.OnFault = func(p wal.FaultPoint) error {
		return inj.Check(mapWALPoint(p))
	}
}

func mapWALPoint(p wal.FaultPoint) FaultPoint {
	switch p {
	case wal.FaultBeforeAppend:
		return FaultBeforeWALAppend
	case wal.FaultDuringWrite:
		return FaultDuringWALWrite
	case wal.FaultBeforeSync:
		return FaultBeforeWALSync
	case wal.FaultAfterSync:
		return FaultAfterWALSync
	default:
		return FaultPoint(fmt.Sprintf("wal.unknown(%d)", int(p)))
	}
}

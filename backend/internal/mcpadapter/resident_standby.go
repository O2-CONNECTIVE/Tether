package mcpadapter

import "context"

// ResidentStandbyBackend optionally reads the standby of this session's
// enrolled host (#412). It is read-only: there is no port to extend standby.
type ResidentStandbyBackend interface {
	StandbyStatus(context.Context) map[string]any
}

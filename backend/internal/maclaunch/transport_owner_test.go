package maclaunch_test

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
)

// The real existing approval owner implements the shared port without making
// agenttransport production source depend on this Mac-specific owner package.
var _ agenttransport.Owner = (*maclaunch.Approved)(nil)

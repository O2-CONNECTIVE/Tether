//go:build darwin && cgo && ci_actual_producer

package main

import (
	"fmt"
	"io"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentclient"
)

func foreground(diagnostic io.Writer) int {
	if result:=residentclient.CIWorkMain();result!=0 {
		fmt.Fprintln(diagnostic,"CI_WORK_UNCONFIRMED")
		return result
	}
	return 0
}

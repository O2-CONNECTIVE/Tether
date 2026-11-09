package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWorkHelpStatesFixedSessionAndTrustBoundary(t *testing.T) {
	var out,diagnostic bytes.Buffer
	if execute([]string{"--help"},&out,&diagnostic)!=0 || diagnostic.Len()!=0 {t.Fatal("help refused")}
	for _,word:=range []string{"start <request_id>","result <request_id>","cancel <request_id>","release <request_id>","close","idempotency","does not cancel","unavailable","CI","pipes"} {
		if !strings.Contains(out.String(),word) {t.Fatal("missing boundary",word)}
	}
}

func TestWorkOrdinaryEntryRemainsUnavailable(t *testing.T) {
	var out,diagnostic bytes.Buffer
	if execute([]string{"foreground"},&out,&diagnostic)!=2 || out.Len()!=0 || !strings.Contains(diagnostic.String(),"WORK_SOURCE_UNAVAILABLE") {t.Fatal("ordinary trust gate changed")}
}

func TestWorkRejectsOverrideArgumentsBeforeEntry(t *testing.T) {
	for _,args:=range [][]string{nil,{"start","1"},{"foreground","--token","fixture"},{"foreground","--endpoint","https://invalid"},{"foreground","--ca","fixture"},{"--help","foreground"}} {
		var out,diagnostic bytes.Buffer
		if execute(args,&out,&diagnostic)!=2 || out.Len()!=0 || strings.Contains(diagnostic.String(),"WORK_SOURCE_UNAVAILABLE") {t.Fatal("invalid args reached entry")}
	}
}

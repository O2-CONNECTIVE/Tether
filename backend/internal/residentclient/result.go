package residentclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

// The four operations use the existing residentOutputSchema. Never forward an
// arbitrary MCP error, text annotation, unknown field or partially decoded result.
func decodeTool(raw []byte, op, id string) (json.RawMessage, error) {
	if len(raw)>mcpadapter.MaxLine {return nil,ErrProtocol}
	m,e:=fields(raw,"content","structuredContent","isError")
	if e!=nil || !exact(m,"content","structuredContent","isError") {return nil,ErrProtocol}
	failed,ok:=boolean(m["isError"]);if !ok{return nil,ErrProtocol}
	var content []json.RawMessage
	if json.Unmarshal(m["content"],&content)!=nil || len(content)!=1 {return nil,ErrProtocol}
	c,e:=fields(content[0],"type","text")
	if e!=nil || !exact(c,"type","text") || !enum(c["type"],"text") {return nil,ErrProtocol}
	mirror,ok:=str(c["text"]);if !ok || len(mirror)>mcpadapter.MaxLine {return nil,ErrProtocol}
	v,e:=fields(m["structuredContent"])
	if e!=nil || validValue(v,op,id)!=nil {return nil,ErrProtocol}
	code,ok:=str(v["error"]);if !ok || failed!=(code!="") {return nil,ErrProtocol}
	// Compare parsed JSON values, allowing harmless map ordering differences.
	var left,right any
	for _,part:=range []struct{raw []byte; into *any}{{m["structuredContent"],&left},{[]byte(mirror),&right}} {
		if _,e:=fields(part.raw);e!=nil{return nil,ErrProtocol}
		d:=json.NewDecoder(bytes.NewReader(part.raw));d.UseNumber()
		if d.Decode(part.into)!=nil{return nil,ErrProtocol}
	}
	if !reflect.DeepEqual(left,right) {return nil,ErrProtocol}
	// Re-encode validated fields only; preserve each state rather than infer success.
	b,e:=json.Marshal(v);if e!=nil{return nil,ErrProtocol};return b,nil
}

func validValue(v map[string]json.RawMessage, op,id string) error {
	if !agentwire.ValidCommandID(id) || operation(op)=="" {return ErrProtocol}
	if exact(v,"error") {
		if enum(v["error"],"not_sent","not_released","not_found","request_generation_mismatch","replay_ledger_full","cleanup_not_confirmed") {return nil}
		return ErrProtocol
	}
	if exact(v,"error","owner_cleanup") && enum(v["error"],"busy_or_refused") && enum(v["owner_cleanup"],"unknown") {return nil}
	if exact(v,"error","owner_cleanup","reinitialize_required") && enum(v["error"],"control_ended") && enum(v["owner_cleanup"],"unknown") && string(v["reinitialize_required"])=="true" {return nil}
	names:=[]string{"request_id","phase","profile","command_id","command_phase","exit","stop","terminal","command_cleanup","owner_cleanup","gateway_cleanup","chunks","next_cursor","error"}
	released:=false
	if r,exists:=v["result_released"];exists {
		if string(r)!="true" {return ErrProtocol};released=true;names=append(names,"result_released")
	}
	if !exact(v,names...) || text(v["request_id"])!=id || (op=="release" && !released) {return ErrProtocol}
	if !enum(v["phase"],"pending","running","finished","unknown") || !enum(v["profile"],"","mac_current_user") || !enum(v["command_phase"],"","starting","running","exited","canceled","failed","start_unknown") {return ErrProtocol}
	command,ok:=str(v["command_id"]);if !ok || (command!="" && !agentwire.ValidCommandID(command)) {return ErrProtocol}
	if !enum(v["stop"],"none","canceled","command_timeout","revoked","hard_deadline","transport_lost","runtime_fault") || !enum(v["command_cleanup"],"","pending","confirmed","unknown","not_required") {return ErrProtocol}
	for _,f:=range []string{"owner_cleanup","gateway_cleanup"} {if !enum(v[f],"pending","confirmed","unknown"){return ErrProtocol}}
	if _,ok:=boolean(v["terminal"]);!ok{return ErrProtocol}
	if _,ok:=counter(v["next_cursor"]);!ok{return ErrProtocol}
	if !enum(v["error"],"","admission_refused_or_uncertain","registration_unobserved","start_uncertain","output_limit","result_unobserved_or_uncertain") {return ErrProtocol}
	exit,e:=fields(v["exit"],"known","code");if e!=nil{return ErrProtocol}
	known,ok:=boolean(exit["known"]);if !ok{return ErrProtocol}
	if known {
		n,e:=strconv.ParseInt(string(exit["code"]),10,32)
		if !exact(exit,"known","code") || e!=nil || strconv.FormatInt(n,10)!=string(exit["code"]) {return ErrProtocol}
	} else if !exact(exit,"known") {return ErrProtocol}
	var chunks []json.RawMessage
	if len(v["chunks"])==0 || v["chunks"][0]!='[' || json.Unmarshal(v["chunks"],&chunks)!=nil || len(chunks)>128 {return ErrProtocol}
	for _,raw:=range chunks {
		c,e:=fields(raw,"stream","offset","encoding","data")
		if e!=nil || !exact(c,"stream","offset","encoding","data") || !enum(c["stream"],"stdout","stderr") || !enum(c["encoding"],"base64") {return ErrProtocol}
		if _,ok:=counter(c["offset"]);!ok{return ErrProtocol}
		s,ok:=str(c["data"]);if !ok || len(s)>21848{return ErrProtocol}
		data,e:=base64.StdEncoding.Strict().DecodeString(s)
		if e!=nil || len(data)>16384 || base64.StdEncoding.EncodeToString(data)!=s{return ErrProtocol}
	}
	if released && (!enum(v["phase"],"finished") || !enum(v["owner_cleanup"],"confirmed") || !enum(v["gateway_cleanup"],"confirmed") || len(chunks)!=0) {return ErrProtocol}
	return nil
}

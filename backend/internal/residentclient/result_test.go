package residentclient

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func fixtureValue(id string) map[string]any {
	return map[string]any{"request_id":id,"phase":"pending","profile":"","command_id":"","command_phase":"","exit":map[string]any{"known":false},"stop":"none","terminal":false,"command_cleanup":"pending","owner_cleanup":"pending","gateway_cleanup":"pending","chunks":[]any{},"next_cursor":0,"error":""}
}

func TestResultCountersPreserveFullUint64WithoutNarrowingRPCIDs(t *testing.T) {
	for _,n:=range []uint64{0,maxInteger,maxInteger+1,^uint64(0)} {
		v:=fixtureValue(testID)
		v["next_cursor"]=n
		v["chunks"]=[]any{map[string]any{"stream":"stdout","offset":n,"encoding":"base64","data":""}}
		out,e:=decodeTool(fixtureTool(v),"result",testID)
		if e!=nil || !strings.Contains(string(out),`"next_cursor":`+strconv.FormatUint(n,10)) || !strings.Contains(string(out),`"offset":`+strconv.FormatUint(n,10)) {t.Fatal("output counter narrowed",n,e)}
	}
	for _,raw:=range []string{"-1","1.5","18446744073709551616"} {
		v:=fixtureValue(testID);v["next_cursor"]=json.Number(raw)
		if _,e:=decodeTool(fixtureTool(v),"result",testID);e==nil {t.Fatal("invalid counter accepted",raw)}
		v["next_cursor"]=0
		v["chunks"]=[]any{map[string]any{"stream":"stdout","offset":json.Number(raw),"encoding":"base64","data":""}}
		if _,e:=decodeTool(fixtureTool(v),"result",testID);e==nil {t.Fatal("invalid offset accepted",raw)}
	}
	if _,ok:=integer(json.RawMessage("9007199254740992"));ok {t.Fatal("RPC ID bound expanded")}
}
func fixtureTool(v map[string]any) []byte {
	b,_ := json.Marshal(v); code,_:=v["error"].(string)
	r,_ := json.Marshal(map[string]any{"structuredContent":v,"content":[]any{map[string]any{"type":"text","text":string(b)}},"isError":code!=""});return r
}
func TestResultPreservesIndependentStateAndKnownErrors(t *testing.T) {
	id:=strings.Repeat("1",32)
	for _,v:=range []map[string]any{fixtureValue(id),{"error":"not_found"},{"error":"control_ended","owner_cleanup":"unknown","reinitialize_required":true}} {
		if _,err:=decodeTool(fixtureTool(v),"result",id);err!=nil {t.Fatal("existing fixed schema rejected",err)}
	}
	v:=fixtureValue(id);v["phase"]="finished";v["terminal"]=false;v["owner_cleanup"]="confirmed";v["gateway_cleanup"]="confirmed";v["result_released"]=true
	if _,err:=decodeTool(fixtureTool(v),"release",id);err!=nil {t.Fatal("release invented command execution",err)}
}
func TestResultRejectsUnknownFieldsCorrelationAndContradictions(t *testing.T) {
	id:=strings.Repeat("1",32)
	for _,change:=range []func(map[string]any){
		func(v map[string]any){v["token"]="private"},func(v map[string]any){v["request_id"]=strings.Repeat("2",32)},
		func(v map[string]any){v["exit"]=map[string]any{"known":false,"code":0}},func(v map[string]any){v["phase"]="success"},
		func(v map[string]any){v["result_released"]=true},func(v map[string]any){v["chunks"]=nil},
		func(v map[string]any){v["next_cursor"]=1.5},func(v map[string]any){delete(v,"terminal")},
	} {v:=fixtureValue(id);change(v);if _,e:=decodeTool(fixtureTool(v),"result",id);e==nil {t.Fatal("invalid result accepted")}}
	raw:=fixtureTool(fixtureValue(id));raw=[]byte(strings.Replace(string(raw),`"isError":false`,`"isError":true`,1))
	if _,e:=decodeTool(raw,"result",id);e==nil {t.Fatal("isError mismatch accepted")}
	if _,e:=decodeTool([]byte(`{"structuredContent":{},"structuredContent":{}}`),"result",id);e==nil {t.Fatal("duplicate accepted")}
}

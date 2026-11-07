package mcpadapter_test

import (
 "encoding/json"
 "fmt"
 "math"
 "reflect"
 "regexp"
 "testing"
)

// checkOutputShape implements only the keywords emitted by this catalog. It is
// a fail-closed fixture checker, not a general JSON Schema implementation.
func checkOutputShape(schema map[string]any, value any) error {
 for key := range schema {
  switch key {
  case "type", "properties", "required", "additionalProperties", "oneOf", "enum", "minimum", "maximum", "pattern", "items", "description":
  default: return fmt.Errorf("unsupported schema keyword %s", key)
  }
 }
 if alternatives, ok := schema["oneOf"].([]any); ok {
  matches := 0
  for _, alternative := range alternatives {
   if checkOutputShape(alternative.(map[string]any), value) == nil { matches++ }
  }
  if matches != 1 { return fmt.Errorf("oneOf matches %d branches", matches) }
 }
 if values, ok := schema["enum"].([]any); ok {
  found := false
  for _, allowed := range values { found = found || reflect.DeepEqual(value, allowed) }
  if !found { return fmt.Errorf("not in enum: %v", value) }
 }
 switch schema["type"] {
 case "object":
  object, ok := value.(map[string]any)
  if !ok { return fmt.Errorf("not object") }
  props, _ := schema["properties"].(map[string]any)
  if required, ok := schema["required"].([]any); ok {
   for _, key := range required { if _, exists := object[key.(string)]; !exists { return fmt.Errorf("missing %s", key) } }
  }
  for key, v := range object {
   child, exists := props[key]
   if !exists { if schema["additionalProperties"] == false { return fmt.Errorf("unknown %s", key) }; continue }
   if err := checkOutputShape(child.(map[string]any), v); err != nil { return fmt.Errorf("%s: %w", key, err) }
  }
 case "array":
  array, ok := value.([]any); if !ok { return fmt.Errorf("not array") }
  for _, v := range array { if err := checkOutputShape(schema["items"].(map[string]any), v); err != nil { return err } }
 case "string":
  s, ok := value.(string); if !ok { return fmt.Errorf("not string") }
  if pattern, ok := schema["pattern"].(string); ok { matched, err := regexp.MatchString(pattern, s); if err != nil || !matched { return fmt.Errorf("pattern mismatch") } }
 case "boolean":
  if _, ok := value.(bool); !ok { return fmt.Errorf("not boolean") }
 case "integer":
  n, ok := value.(float64); if !ok || math.Trunc(n) != n { return fmt.Errorf("not integer") }
  if min, ok := schema["minimum"].(float64); ok && n < min { return fmt.Errorf("below minimum") }
  if max, ok := schema["maximum"].(float64); ok && n > max { return fmt.Errorf("above maximum") }
 case nil:
 default: return fmt.Errorf("unsupported type")
 }
 return nil
}

func TestPublishedOutputSchemasMatchExistingResults(t *testing.T) {
 h := newHarness(t, options{})
 h.initialize()
 catalog := h.request("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
 schemas := map[string]map[string]any{}
 for _, entry := range catalog {
  tool := entry.(map[string]any)
  schema, ok := tool["outputSchema"].(map[string]any)
  if !ok || schema["type"] != "object" { t.Fatalf("%s lacks object outputSchema", tool["name"]) }
  schemas[tool["name"].(string)] = schema
 }
 verify := func(name string, args any) map[string]any {
  t.Helper()
  value, _ := h.tool(name, args)
  if err := checkOutputShape(schemas[name], value); err != nil { t.Fatalf("%s: %v (%v)", name, err, value) }
  return value
 }
 start := verify("rescue_start", map[string]any{"request_id":"schema", "command":"fixed", "timeout_ms":1000})
 id := commandID(t, start)
 ref := map[string]any{"command_id":id}
 verify("rescue_status", ref)
 verify("rescue_output", map[string]any{"command_id":id,"cursor":0,"max_bytes":64})
 verify("rescue_cancel", ref)
 for _, name := range []string{"rescue_status", "rescue_cancel"} { verify(name, map[string]any{"command_id":foreignID}) }
 verify("rescue_output", map[string]any{"command_id":foreignID,"cursor":0,"max_bytes":64})
 verify("rescue_start", map[string]any{"request_id":"schema","command":"different","timeout_ms":1000})
 for _, mutation := range []func(map[string]any){
  func(v map[string]any){ delete(v,"cleanup") },
  func(v map[string]any){ v["cleanup"] = "guaranteed" },
  func(v map[string]any){ v["profile"] = "sandbox" },
  func(v map[string]any){ v["terminal"] = "true" },
  func(v map[string]any){ v["unexpected"] = true },
  func(v map[string]any){ v["exit"] = map[string]any{"known":false,"code":0.0} },
  func(v map[string]any){ v["exit"] = map[string]any{"known":true} },
  func(v map[string]any){ v["output_end"] = -1.0 },
 } {
  raw,_ := json.Marshal(start); var bad map[string]any; _ = json.Unmarshal(raw,&bad); mutation(bad)
  if checkOutputShape(schemas["rescue_start"], bad) == nil { t.Fatalf("accepted invalid status %v",bad) }
 }
}

use super::wire::Event;
use serde_json::{json, Value};

pub(super) fn status(source: u64) -> Value {
    json!({"protocolVersion":1,"type":"status","sourceContinuousMs":source,
        "snapshot":{"protocolVersion":1,"source":"resident-owner","state":"source-observation",
        "revision":1,"validForMs":3000,"standby":"off","registration":"disconnected",
        "caller":null,"work":"idle","cleanup":"confirmed","canRequestOn":true,
        "canRequestOff":false,"transition":null}})
}
pub(super) fn terminal(source: u64, outcome: &str) -> Value {
    json!({"protocolVersion":1,"type":"closed","sourceContinuousMs":source,"outcome":outcome})
}
pub(super) fn receipt() -> Value {
    json!({"protocolVersion":1,"type":"receipt","receipt":{"protocolVersion":1,
        "requestId":1,"intent":"off","disposition":"accepted"}})
}
pub(super) fn rejection() -> Value {
    json!({"protocolVersion":1,"type":"startupRejected","sourceContinuousMs":100,
        "code":"policy-rejected"})
}
pub(super) fn event(value: &Value) -> Event {
    Event::parse(&serde_json::to_vec(value).unwrap()).unwrap()
}

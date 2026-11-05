//! Injected read-completion timing on captured harmless child pipes.
use super::*;
use crate::resident_port::duplex::test_frames::status_line as status;
use serde_json::json;
use std::process::{Command, Stdio};
#[test]
fn completed_frame_cannot_bypass_deadline_between_pre_read_and_post_read() {
    for completed_at in [1099, 1100, 2100] {
        let mut child = Command::new("/bin/sh")
            .args(["-c", "IFS= read -r line"])
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        let mut pump = Pump::take(&mut child).unwrap();
        let mut shared = Shared::default();
        let frame = format!("{}\n", status(1, 100, json!(null)));
        pump.consume(&mut shared, &frame.as_bytes()[..1], 100)
            .unwrap();
        assert!(pump.check_partial(1099).is_ok()); // pre-read sample
        let completed = pump.consume(&mut shared, &frame.as_bytes()[1..], completed_at);
        assert_eq!(completed.is_ok(), completed_at < 1100);
        assert_eq!(shared.current(completed_at).is_some(), completed_at < 1100);
        drop(pump);
        child.wait().unwrap();
    }
}

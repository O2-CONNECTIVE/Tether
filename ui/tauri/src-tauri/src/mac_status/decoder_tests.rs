use super::{decode, Invalid, MAX_LINE_BYTES};

const VALID: &str = r#"{"version":1,"source_continuous_ms":1000,"seq":1,"phase":"connected","reason":"none","grant_remaining_ms":5000,"starting_workers":0,"running_workers":1,"stopping_workers":0,"cleanup":"not_started","terminal_cleanup_unknown":false,"terminal":false}"#;

fn changed(changes: &[(&str, &str)]) -> String {
    changes.iter().fold(VALID.to_owned(), |input, (from, to)| {
        assert!(input.contains(from), "fixture replacement must match");
        input.replace(from, to)
    })
}

fn rejects(input: &str) {
    assert_eq!(decode(input.as_bytes()), Err(Invalid));
}

fn failed(reason: &str, cleanup: &str, remaining: &str) -> String {
    changed(&[
        (r#""phase":"connected""#, r#""phase":"failed""#),
        (r#""reason":"none""#, &format!(r#""reason":"{reason}""#)),
        (r#""cleanup":"not_started""#, &format!(r#""cleanup":"{cleanup}""#)),
        (r#""grant_remaining_ms":5000"#, &format!(r#""grant_remaining_ms":{remaining}"#)),
        (r#""running_workers":1"#, r#""running_workers":0"#),
        (r#""terminal":false"#, r#""terminal":true"#),
    ])
}

#[test]
fn decodes_exact_fields_and_whitespace() {
    let frame = decode(VALID.as_bytes()).unwrap();
    assert_eq!(frame.source_ms, 1000);
    assert_eq!(frame.seq, 1);
    assert_eq!(frame.phase, "connected");
    assert_eq!(frame.reason, "none");
    assert_eq!(frame.remaining, Some(5000));
    assert_eq!((frame.starting, frame.running, frame.stopping), (0, 1, 0));
    assert_eq!(frame.cleanup, "not_started");
    assert!(!frame.terminal_unknown && !frame.terminal);
    let spaced = format!(" \t\r\n{} \r\n\t", VALID.replace(':', " : ").replace(',', ",\n"));
    assert_eq!(decode(spaced.as_bytes()), Ok(frame));
}

#[test]
fn rejects_duplicate_missing_extra_and_escaped_keys() {
    rejects(&VALID.replacen('{', r#"{"version":1,"#, 1));
    rejects(&VALID.replace(r#""version":1,"#, ""));
    rejects(&VALID.replace(r#""version":1"#, r#""extra":1"#));
    rejects(&VALID.replace(r#""version":1"#, r#""version":1,"extra":1"#));
    rejects(&VALID.replace("version", r"ver\u0073ion"));
    rejects(&VALID.replace("connected", r"connec\u0074ed"));
}

#[test]
fn rejects_nonflat_or_malformed_input_without_payload_errors() {
    for input in ["", "{}", "[]", "null", "true", "{", "\u{feff}", " \t\r\n"] {
        rejects(input);
    }
    for value in ["[]", "{}", "\"1\"", "null", "true", "false", "-1", "+1",
        "01", "1.0", "1e0", "18446744073709551616", "NaN"] {
        rejects(&VALID.replace(r#""seq":1"#, &format!(r#""seq":{value}"#)));
    }
    for value in ["TRUE", "0", "\"false\"", "null", "falsex"] {
        rejects(&VALID.replace(r#""terminal":false"#, &format!(r#""terminal":{value}"#)));
    }
    rejects(&format!("{VALID}x"));
    rejects(&format!("{VALID}{VALID}"));
    rejects(&VALID.replace('}', ",}"));
    rejects(&VALID.replace("connected", "connectéd"));
    rejects(&VALID.replace("connected", "connect\u{7f}ed"));
    rejects(&VALID.replace("connected", "connect\ned"));
    rejects(&format!("\u{b}{VALID}"));
    let mut invalid_utf8 = VALID.as_bytes().to_vec();
    invalid_utf8[2] = 0xff;
    assert_eq!(decode(&invalid_utf8), Err(Invalid));
}

#[test]
fn enforces_length_and_safe_integer_boundaries() {
    let exact = format!("{VALID}{}", " ".repeat(MAX_LINE_BYTES - VALID.len()));
    assert!(decode(exact.as_bytes()).is_ok());
    rejects(&format!("{exact} "));
    assert!(decode(changed(&[(r#""source_continuous_ms":1000"#,
        r#""source_continuous_ms":0"#)]).as_bytes()).is_ok());
    for key in ["seq", "source_continuous_ms"] {
        let old = if key == "seq" { "\"seq\":1" } else { "\"source_continuous_ms\":1000" };
        assert!(decode(VALID.replace(old, &format!("\"{key}\":9007199254740991")).as_bytes()).is_ok());
        rejects(&VALID.replace(old, &format!("\"{key}\":9007199254740992")));
    }
    rejects(&VALID.replace("\"seq\":1", "\"seq\":0"));
    for version in ["0", "2", "true", "\"1\""] {
        rejects(&VALID.replace("\"version\":1", &format!("\"version\":{version}")));
    }
    for value in ["null", "0", "86400000"] {
        assert!(decode(VALID.replace("\"grant_remaining_ms\":5000",
            &format!("\"grant_remaining_ms\":{value}")).as_bytes()).is_ok());
    }
    rejects(&VALID.replace("\"grant_remaining_ms\":5000", "\"grant_remaining_ms\":86400001"));
}

#[test]
fn validates_enums_workers_and_terminal_equivalence() {
    for (old, new) in [("\"connected\"", "\"unexpected\""), ("\"none\"", "\"unexpected\""),
        ("\"not_started\"", "\"unexpected\""), ("\"running_workers\":1", "\"running_workers\":2"),
        ("\"starting_workers\":0", "\"starting_workers\":1"),
        ("\"stopping_workers\":0", "\"stopping_workers\":1"),
        ("\"terminal\":false", "\"terminal\":true"),
        ("\"connected\"", "\"closed\""), ("\"connected\"", "\"failed\"")] {
        rejects(&VALID.replace(old, new));
    }
    for phase in ["checking_config", "loading_key", "preparing", "awaiting_approval",
        "connecting", "connected", "stopping"] {
        assert!(decode(VALID.replace("connected", phase).as_bytes()).is_ok());
    }
    for phase in ["closed", "failed"] {
        let input = VALID.replace("connected", phase).replace("\"terminal\":false", "\"terminal\":true");
        assert!(decode(input.as_bytes()).is_ok());
    }
}

#[test]
fn enforces_unknown_implication_not_an_invented_cleanup_equivalence() {
    let unknown = failed("terminal_cleanup_unknown", "unknown", "null")
        .replace("\"terminal_cleanup_unknown\":false", "\"terminal_cleanup_unknown\":true");
    assert!(decode(unknown.as_bytes()).unwrap().terminal_unknown);
    for (old, new) in [("\"phase\":\"failed\"", "\"phase\":\"closed\""),
        ("\"cleanup\":\"unknown\"", "\"cleanup\":\"pending\""),
        ("\"reason\":\"terminal_cleanup_unknown\"", "\"reason\":\"none\""),
        ("\"terminal_cleanup_unknown\":true", "\"terminal_cleanup_unknown\":false")] {
        rejects(&unknown.replace(old, new));
    }
    rejects(&VALID.replace("\"terminal_cleanup_unknown\":false", "\"terminal_cleanup_unknown\":true"));
    // Swift allows cleanup=unknown without asserting terminal_cleanup_unknown.
    assert!(decode(VALID.replace("\"not_started\"", "\"unknown\"").as_bytes()).is_ok());
}

#[test]
fn enforces_all_setup_failure_constraints() {
    for reason in ["config_error", "key_error", "preparation_error", "connection_error",
        "approval_not_completed"] {
        for cleanup in ["not_started", "confirmed"] {
            for remaining in ["null", "0"] {
                assert!(decode(failed(reason, cleanup, remaining).as_bytes()).is_ok());
            }
        }
        rejects(&failed(reason, "pending", "0"));
        rejects(&failed(reason, "unknown", "0"));
        rejects(&failed(reason, "confirmed", "1"));
        rejects(&failed(reason, "not_started", "null").replace("\"running_workers\":0", "\"running_workers\":1"));
        rejects(&failed(reason, "not_started", "0").replace("\"phase\":\"failed\"", "\"phase\":\"closed\""));
        rejects(&VALID.replace("\"reason\":\"none\"", &format!("\"reason\":\"{reason}\"")));
    }
}

#[test]
fn confirmed_terminal_requires_no_workers_and_no_remaining() {
    let terminal = failed("none", "confirmed", "0");
    assert!(decode(terminal.as_bytes()).is_ok());
    rejects(&terminal.replace("\"running_workers\":0", "\"running_workers\":1"));
    rejects(&terminal.replace("\"grant_remaining_ms\":0", "\"grant_remaining_ms\":1"));
    assert!(decode(failed("none", "confirmed", "null").as_bytes()).is_ok());
    // These restrictions are conditional on terminal, as in Swift.
    assert!(decode(VALID.replace("\"not_started\"", "\"confirmed\"").as_bytes()).is_ok());
}

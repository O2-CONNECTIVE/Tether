use super::state::{FeedFault, FeedState};

fn fault(value: &str) -> FeedFault {
    match value {
        "unavailable" => FeedFault::Unavailable,
        "stale" => FeedFault::Stale,
        "malformed" => FeedFault::Malformed,
        "clock" => FeedFault::Clock,
        "sequence" => FeedFault::Sequence,
        "eof" => FeedFault::Eof,
        "read" => FeedFault::Read,
        "rate" => FeedFault::Rate,
        "stopped" => FeedFault::Stopped,
        _ => panic!("closed fixture fault"),
    }
}

#[test]
fn matches_independently_checked_swift_golden() {
    let fixture = include_str!("../../tests/fixtures/mac_status_golden.tsv");
    let mut state = FeedState::default();
    let mut checked = 0;
    for line in fixture.lines().filter(|line| !line.starts_with('#')) {
        let fields: Vec<_> = line.split('\t').collect();
        assert_eq!(fields.len(), 13);
        let now = match fields[2] {
            "nan" => f64::NAN,
            "inf" => f64::INFINITY,
            value => value.parse::<f64>().expect("closed fixture clock"),
        };
        match fields[1] {
            "reset" => state = FeedState::default(),
            "query" => {}
            "ingest" => {
                let hex = fields[3];
                let bytes = if hex == "-" {
                    Vec::new()
                } else {
                    assert_eq!(hex.len() % 2, 0);
                    (0..hex.len())
                        .step_by(2)
                        .map(|offset| u8::from_str_radix(&hex[offset..offset + 2], 16).expect("closed fixture hex"))
                        .collect()
                };
                state.ingest(&bytes, now);
            }
            op if op.starts_with("fail:") => state.fail(fault(&op[5..])),
            _ => panic!("closed fixture operation"),
        }
        let s = state.snapshot();
        let actual = [
            s.fault.map_or("-", FeedFault::as_str).to_owned(),
            s.frame.as_ref().map_or("-", |frame| frame.phase.as_str()).to_owned(),
            s.frame.as_ref().map_or_else(|| "-".to_owned(), |frame| frame.seq.to_string()),
            s.frame.as_ref().map_or("-", |frame| frame.cleanup.as_str()).to_owned(),
            u8::from(s.sticky_unknown).to_string(),
            u8::from(s.source_closed()).to_string(),
            u8::from(s.terminal_observed()).to_string(),
            u8::from(s.fresh(now)).to_string(),
            s.remaining(now).map_or_else(|| "-".to_owned(), |remaining| remaining.to_string()),
        ];
        assert_eq!(actual.join("\t"), fields[4..].join("\t"), "case={} op={}", fields[0], fields[1]);
        checked += 1;
    }
    assert_eq!(checked, 156);
}

use super::{Input, Provider, ReadStep};
use crate::mac_status::state::FeedFault;
use std::cell::Cell;
use std::collections::VecDeque;
use std::rc::Rc;
enum Step {
    Data(Vec<u8>),
    Pending,
    Eof,
    Error,
}
struct Fake {
    steps: VecDeque<Step>,
    drops: Rc<Cell<u32>>,
}
impl Drop for Fake {
    fn drop(&mut self) {
        self.drops.set(self.drops.get() + 1);
    }
}
impl Input for Fake {
    fn read_once(&mut self, out: &mut [u8; 4096]) -> Result<ReadStep, ()> {
        match self.steps.pop_front().unwrap_or(Step::Pending) {
            Step::Data(v) => {
                out[..v.len()].copy_from_slice(&v);
                Ok(ReadStep::Bytes(v.len()))
            }
            Step::Pending => Ok(ReadStep::Pending),
            Step::Eof => Ok(ReadStep::Eof),
            Step::Error => Err(()),
        }
    }
}
fn frame(seq: u64, time: u64, terminal: bool) -> Vec<u8> {
    format!(r#"{{"version":1,"source_continuous_ms":{time},"seq":{seq},"phase":"{}","reason":"none","grant_remaining_ms":{},"starting_workers":0,"running_workers":0,"stopping_workers":0,"cleanup":"{}","terminal_cleanup_unknown":false,"terminal":{terminal}}}
"#,if terminal {"closed"}else{"connected"},if terminal{0}else{2000},if terminal{"confirmed"}else{"pending"}).into_bytes()
}
fn make(
    steps: Vec<Step>,
    time: Rc<Cell<f64>>,
    drops: Rc<Cell<u32>>,
) -> Provider<Fake, impl FnMut() -> Option<f64>> {
    Provider::new(
        Fake {
            steps: steps.into(),
            drops,
        },
        move || Some(time.get()),
    )
    .unwrap()
}
#[test]
fn closed_provider_lifecycle_and_clock_cases() {
    let mut checks = 0;
    let time = Rc::new(Cell::new(100.0));
    let drops = Rc::new(Cell::new(0));
    let mut p = make(
        vec![
            Step::Pending,
            Step::Data(frame(1, 100000, false)),
            Step::Pending,
        ],
        time.clone(),
        drops.clone(),
    );
    p.pump_once();
    assert!(p.observation().0.frame.is_none());
    assert_eq!(drops.get(), 0);
    checks += 1;
    p.pump_once();
    assert!(p.observation().0.fresh(100.0));
    checks += 1;
    time.set(102.0);
    let (s, _) = p.observation();
    assert_eq!(s.remaining(102.0), Some(0));
    checks += 1;
    time.set(103.001);
    assert!(!p.observation().0.fresh(103.001));
    checks += 1;
    time.set(99.0);
    let (s, t) = p.observation();
    assert_eq!(t, None);
    assert_eq!(s.fault, Some(FeedFault::Clock));
    assert_eq!(drops.get(), 1);
    checks += 1;
    time.set(100.0);
    let f = frame(1, 100000, false);
    let cut = f.len() / 2;
    let mut p = make(
        vec![Step::Data(f[..cut].to_vec()), Step::Data(f[cut..].to_vec())],
        time.clone(),
        Rc::new(Cell::new(0)),
    );
    p.pump_once();
    assert!(p.observation().0.frame.is_none());
    p.pump_once();
    assert!(p.observation().0.fresh(100.0));
    checks += 1;
    let mut p = make(
        vec![Step::Data(b"{".to_vec()), Step::Eof],
        time.clone(),
        Rc::new(Cell::new(0)),
    );
    p.pump_once();
    p.pump_once();
    assert_eq!(p.observation().0.fault, Some(FeedFault::Malformed));
    checks += 1;
    let mut p = make(
        vec![Step::Data(frame(1, 100000, true)), Step::Eof],
        time.clone(),
        Rc::new(Cell::new(0)),
    );
    p.pump_once();
    p.pump_once();
    let (s, _) = p.observation();
    assert!(s.source_closed() && s.terminal_observed());
    assert!(!s.fresh(100.0));
    checks += 1;
    let mut p = make(
        vec![
            Step::Data(frame(1, 96000, false)),
            Step::Data(frame(2, 100000, false)),
        ],
        time.clone(),
        Rc::new(Cell::new(0)),
    );
    p.pump_once();
    p.pump_once();
    assert_eq!(p.observation().0.fault, Some(FeedFault::Stale));
    checks += 1;
    let mut p = make(
        vec![
            Step::Data(frame(1, 100000, false)),
            Step::Data(frame(1, 100000, false)),
        ],
        time.clone(),
        Rc::new(Cell::new(0)),
    );
    p.pump_once();
    p.pump_once();
    assert_eq!(p.observation().0.fault, Some(FeedFault::Sequence));
    checks += 1;
    let drops = Rc::new(Cell::new(0));
    let mut p = make(vec![Step::Error], time.clone(), drops.clone());
    p.pump_once();
    assert_eq!(p.observation().0.fault, Some(FeedFault::Read));
    assert_eq!(drops.get(), 1);
    checks += 1;
    let drops = Rc::new(Cell::new(0));
    let mut p = make(vec![], time.clone(), drops.clone());
    p.stop();
    p.stop();
    assert_eq!(drops.get(), 1);
    assert_eq!(p.observation().0.fault, Some(FeedFault::Stopped));
    checks += 1;
    let drops = Rc::new(Cell::new(0));
    let p = Provider::new(
        Fake {
            steps: VecDeque::new(),
            drops: drops.clone(),
        },
        || None,
    );
    assert!(p.is_none());
    assert_eq!(drops.get(), 1);
    checks += 1;
    let time = Rc::new(Cell::new(100.0));
    let mut p = make(
        vec![
            Step::Data(frame(1, 100000, false)),
            Step::Data(frame(2, 104000, false)),
        ],
        time.clone(),
        Rc::new(Cell::new(0)),
    );
    p.pump_once();
    time.set(104.0);
    assert!(!p.observation().0.fresh(104.0));
    p.pump_once();
    let (s, _) = p.observation();
    assert!(s.fresh(104.0));
    assert_eq!(s.remaining(104.0), Some(0));
    checks += 1;
    let max = 9_007_199_254_740_991u64;
    let now = max as f64 / 1000.0;
    let time = Rc::new(Cell::new(now));
    let mut p = make(
        vec![Step::Data(frame(1, max, false))],
        time.clone(),
        Rc::new(Cell::new(0)),
    );
    p.pump_once();
    let (s, t) = p.observation();
    assert!(s.fresh(t.unwrap()));
    assert_eq!(s.remaining(now + 2.0), Some(0));
    time.set(now + 3.001);
    assert!(!p.observation().0.fresh(time.get()));
    checks += 1;
    for invalid in [f64::NAN, f64::INFINITY, -1.0] {
        let drops = Rc::new(Cell::new(0));
        let p = Provider::new(
            Fake {
                steps: VecDeque::new(),
                drops: drops.clone(),
            },
            || Some(invalid),
        );
        assert!(p.is_none());
        assert_eq!(drops.get(), 1);
        checks += 1;
    }
    let drops = Rc::new(Cell::new(0));
    let mut times = VecDeque::from([Some(100.0), Some(100.0), None]);
    let mut p = Provider::new(
        Fake {
            steps: VecDeque::from([Step::Data(frame(1, 100000, false))]),
            drops: drops.clone(),
        },
        move || times.pop_front().flatten(),
    )
    .unwrap();
    p.pump_once();
    assert_eq!(p.observation().0.fault, Some(FeedFault::Clock));
    assert_eq!(drops.get(), 1);
    checks += 1;
    println!("PASS: {checks} closed provider checks; native FIFO/clock not invoked");
}

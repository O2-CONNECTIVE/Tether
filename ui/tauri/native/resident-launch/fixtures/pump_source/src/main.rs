// This fixture imports real source without installing its UI/IPC entry points.
#![allow(
    dead_code,
    reason = "source components have no production binding in this fixture"
)]
mod mac_status;
#[path = "../../../rust/owner.rs"]
mod native_preparation;
mod resident_port;
#[cfg(resident_native_source)]
#[path = "../../../../../src-tauri/src/standby_lifecycle.rs"]
mod standby_lifecycle;

fn require_duplex<T: std::io::Read + std::io::Write>() {}

fn main() {
    require_duplex::<native_preparation::PreparedIo<'static>>();
    let arguments: Vec<_> = std::env::args().collect();
    #[cfg(all(resident_native_source, resident_launch_fixture))]
    if arguments.get(1).map(String::as_str) == Some("active-child") {
        resident_port::duplex::run_active_child(&arguments[1..]);
        return;
    }
    assert_eq!(arguments.len(), 2);
    assert!(matches!(
        native_preparation::NativePreparation::prepare(),
        native_preparation::Preparation::Refused
    ));
    #[cfg(resident_native_source)]
    if arguments[1].starts_with("outer-") {
        resident_port::duplex::run_outer(&arguments[1]);
        return;
    }
    #[cfg(resident_native_source)]
    if arguments[1].starts_with("supervisor-") {
        resident_port::duplex::run_supervisor(&arguments[1]);
        return;
    }
    #[cfg(all(resident_native_source, resident_launch_fixture))]
    if arguments[1].starts_with("active-") {
        resident_port::duplex::run_active(&arguments[1]);
        return;
    }
    #[cfg(all(resident_native_source, resident_launch_fixture))]
    let passed = resident_port::duplex::run(&arguments[1]);
    #[cfg(not(all(resident_native_source, resident_launch_fixture)))]
    let passed = {
        assert_eq!(arguments[1], "normal");
        1
    };
    println!(
        "NATIVE_PUMP_FIXTURE mode={} passed={passed} failed=0 launched=0",
        arguments[1]
    );
}

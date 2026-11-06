#[cfg(all(resident_native_source, resident_launch_fixture))]
#[path = "checks.rs"]
mod checks;
#[path = "../../../../../src-tauri/src/resident_port/duplex/closure.rs"]
mod closure;
#[path = "../../../../../src-tauri/src/resident_port/duplex/exchange.rs"]
mod exchange;
#[path = "../../../../../src-tauri/src/resident_port/duplex/owner.rs"]
pub(crate) mod owner;
#[path = "../../../../../src-tauri/src/resident_port/duplex/pump.rs"]
mod pump;
#[path = "../../../../../src-tauri/src/resident_port/duplex/startup.rs"]
mod startup;
#[cfg(all(resident_native_source, resident_launch_fixture))]
#[path = "../../../../../src-tauri/src/resident_port/duplex/test_frames.rs"]
mod test_frames;
#[path = "../../../../../src-tauri/src/resident_port/duplex/tracking.rs"]
mod tracking;
#[path = "../../../../../src-tauri/src/resident_port/duplex/wire.rs"]
mod wire;
#[cfg(all(resident_native_source, resident_launch_fixture))]
pub(crate) use checks::run;

#[cfg(all(resident_native_source, resident_launch_fixture))]
#[path = "worker_checks.rs"]
mod worker_checks;

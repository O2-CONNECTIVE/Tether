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

#[cfg(all(resident_native_source, resident_launch_fixture))]
mod active_child;
#[cfg(all(resident_native_source, resident_launch_fixture))]
pub(crate) use active_child::run as run_active_child;
#[cfg(all(resident_native_source, resident_launch_fixture))]
mod active_checks;
#[cfg(all(resident_native_source, resident_launch_fixture))]
pub(crate) use active_checks::run as run_active;

#[cfg(resident_native_source)]
mod supervisor_checks;
#[cfg(resident_native_source)]
pub(crate) use supervisor_checks::run as run_supervisor;

#[cfg(resident_native_source)]
mod outer_checks;
#[cfg(resident_native_source)]
pub(crate) use outer_checks::run as run_outer;

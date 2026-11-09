#[path = "duplex.rs"]
pub(crate) mod duplex;
#[path = "../../../../../src-tauri/src/resident_port/model.rs"]
pub(crate) mod model;
#[path = "../../../../../src-tauri/src/resident_port/observation.rs"]
pub(crate) mod observation;

#[cfg(resident_native_source)]
#[path = "../../../../../src-tauri/src/resident_port/native_session.rs"]
pub(crate) mod native_session;
#[cfg(resident_native_source)]
#[path = "session_checks.rs"]
pub(crate) mod session_checks;

#[cfg(resident_native_source)]
#[path = "../../../../../src-tauri/src/resident_port/port.rs"]
mod port;
#[cfg(resident_native_source)]
pub(crate) use port::ResidentPort;

#[cfg(resident_native_source)]
#[path = "../../../../../src-tauri/src/resident_port/state.rs"]
pub(crate) mod state;
#[cfg(resident_native_source)]
#[path = "gui_state_checks.rs"]
pub(crate) mod gui_state_checks;

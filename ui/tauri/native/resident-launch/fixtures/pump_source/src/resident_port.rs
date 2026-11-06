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

#[path = "../../../../../src-tauri/src/resident_port/port.rs"]
mod port;
pub(crate) use port::ResidentPort;

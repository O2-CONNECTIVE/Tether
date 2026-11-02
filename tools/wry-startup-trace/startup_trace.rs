// Isolated diagnostic instrumentation; not an upstream Wry API.
use std::sync::OnceLock;

/// Fixed events only: no errors, arguments, URLs, profiles, or paths.
#[derive(Clone, Copy)]
pub enum Event {
    /// Diagnostic phase observation.
    EnvironmentRequest,
    /// Diagnostic phase observation.
    EnvironmentCallbackSuccess,
    /// Diagnostic phase observation.
    EnvironmentCallbackError,
    /// Diagnostic phase observation.
    EnvironmentWaitReturnSuccess,
    /// Diagnostic phase observation.
    EnvironmentWaitReturnError,
    /// Diagnostic phase observation.
    ControllerRequest,
    /// Diagnostic phase observation.
    ControllerCallbackSuccess,
    /// Diagnostic phase observation.
    ControllerCallbackError,
    /// Diagnostic phase observation.
    ControllerWaitReturnSuccess,
    /// Diagnostic phase observation.
    ControllerWaitReturnError,
}

type Hook = dyn Fn(Event) + Send + Sync + 'static;
static HOOK: OnceLock<Box<Hook>> = OnceLock::new();

/// Install once, before webview creation. The caller must be nonblocking,
/// allocation-free during invocation, and must not panic.
pub fn set_hook(hook: impl Fn(Event) + Send + Sync + 'static) -> bool {
    HOOK.set(Box::new(hook)).is_ok()
}

pub(crate) fn emit(event: Event) {
    if let Some(hook) = HOOK.get() {
        hook(event);
    }
}

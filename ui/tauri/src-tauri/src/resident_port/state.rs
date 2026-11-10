//! GUI state owns the entire native session; IPC only borrows its original port.
#[cfg(all(target_os = "macos", resident_native_source))]
use super::native_session::NativeSession;
use super::ResidentPort;

pub(crate) struct ResidentState {
    source: Source,
}
enum Source {
    Unbound(ResidentPort),
    #[cfg(all(target_os = "macos", resident_native_source))]
    Native(NativeSession),
    #[cfg(any(test, resident_gui_fixture))]
    Fixture(ResidentPort),
}
impl Default for ResidentState {
    fn default() -> Self {
        Self {
            source: Source::Unbound(ResidentPort::default()),
        }
    }
}
impl ResidentState {
    // No caller input or fallback after entering the native ownership path.
    pub(crate) fn startup() -> std::io::Result<Self> {
        #[cfg(all(target_os = "macos", resident_native_source))]
        {
            Self::prepare_native()
        }
        #[cfg(not(all(target_os = "macos", resident_native_source)))]
        {
            Ok(Self::default())
        }
    }
    // Closed, zero-input bootstrap. No setter, rebind or owner extraction.
    #[cfg(all(target_os = "macos", resident_native_source))]
    pub(crate) fn prepare_native() -> std::io::Result<Self> {
        NativeSession::prepare().map(|session| Self {
            source: Source::Native(session),
        })
    }
    pub(crate) fn port(&self) -> &ResidentPort {
        match &self.source {
            Source::Unbound(port) => port,
            #[cfg(all(target_os = "macos", resident_native_source))]
            Source::Native(session) => session.port(),
            #[cfg(any(test, resident_gui_fixture))]
            Source::Fixture(port) => port,
        }
    }
    pub(crate) fn request_exit(&self) -> bool {
        match &self.source {
            Source::Unbound(_) => true, // This state has never owned a producer.
            #[cfg(all(target_os = "macos", resident_native_source))]
            Source::Native(session) => session.owner().request_exit(),
            // A fixture-only bare port has no owner proof. Its existing keeper
            // handles lifetime separately; this state never grants it Quit.
            #[cfg(any(test, resident_gui_fixture))]
            Source::Fixture(_) => false,
        }
    }
    pub(crate) fn finish_requested(&self) -> bool {
        match &self.source {
            Source::Unbound(_) => true,
            #[cfg(all(target_os = "macos", resident_native_source))]
            Source::Native(session) => session.owner().finish_requested(),
            #[cfg(any(test, resident_gui_fixture))]
            Source::Fixture(_) => false,
        }
    }
    #[cfg(any(test, resident_gui_fixture))]
    pub(crate) fn fixture_port(port: ResidentPort) -> Self {
        Self {
            source: Source::Fixture(port),
        }
    }
    #[cfg(all(target_os = "macos", resident_native_source, resident_launch_fixture))]
    pub(crate) fn fixture_poll_join(
        &self,
    ) -> Option<crate::standby_lifecycle::native::NativeOuterJoined> {
        match &self.source {
            Source::Native(session) => session.owner().poll_join(),
            _ => None,
        }
    }
    #[cfg(all(target_os = "macos", resident_native_source, resident_launch_fixture))]
    pub(crate) fn fixture(
        mode: crate::native_preparation::FixedActiveFixture,
    ) -> std::io::Result<Self> {
        NativeSession::fixture(mode, false).map(|session| Self {
            source: Source::Native(session),
        })
    }
}

// This outer guard is deliberately not Clone. App/IPC may clone only the view
// Arc; they cannot move or substitute the lifetime owner's original state.
pub(crate) struct ResidentLifetime {
    state: std::sync::Arc<ResidentState>,
}
impl ResidentLifetime {
    pub(crate) fn new(state: ResidentState) -> Self {
        Self {
            state: std::sync::Arc::new(state),
        }
    }
    pub(crate) fn state(&self) -> std::sync::Arc<ResidentState> {
        self.state.clone()
    }
}
impl Drop for ResidentLifetime {
    fn drop(&mut self) {
        let _ = self.state.request_exit();
        // Also covers build/setup failure, panic and unexpected loop return.
        // Joining is insufficient. Unknown is retained, never timed out into
        // permission to quit, even if the event loop can no longer make progress.
        while !self.state.finish_requested() {
            std::thread::park_timeout(std::time::Duration::from_secs(1));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn default_is_unbound_and_exit_never_attempts_native_launch() {
        let state = ResidentState::default();
        assert_eq!(
            serde_json::to_value(state.port().current()).unwrap(),
            serde_json::to_value(ResidentPort::default().current()).unwrap()
        );
        assert!(state.request_exit());
        assert!(state.request_exit());
        assert!(state.finish_requested());
    }
    #[test]
    fn unbound_lifetime_covers_return_and_unwind() {
        let view;
        {
            let lifetime = ResidentLifetime::new(ResidentState::default());
            view = lifetime.state();
            assert_eq!(std::sync::Arc::strong_count(&view), 2);
        }
        assert_eq!(std::sync::Arc::strong_count(&view), 1);
        assert!(view.finish_requested());
        fn failed_setup() -> Result<(), &'static str> {
            let _lifetime = ResidentLifetime::new(ResidentState::default());
            Err("anonymous setup failure")
        }
        let setup_failure = failed_setup();
        assert_eq!(setup_failure, Err("anonymous setup failure"));
        let result = std::panic::catch_unwind(|| {
            let _lifetime = ResidentLifetime::new(ResidentState::default());
            panic!("anonymous setup failure");
        });
        assert!(result.is_err());
    }
    #[test]
    fn a_bare_fixture_port_is_not_original_owner_exit_proof() {
        let state = ResidentState::fixture_port(ResidentPort::default());
        assert!(!state.request_exit());
        assert!(!state.finish_requested());
    }
}

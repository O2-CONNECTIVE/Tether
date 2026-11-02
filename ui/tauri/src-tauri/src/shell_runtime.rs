use crate::control_codec::scalar::Code;
use crate::control_coordinator::connection::ReaderConnection;
use crate::control_coordinator::Conversation;
#[cfg(windows)]
use crate::windows_client::client::NativeClient;
use crate::ShellStatus;
use crate::service_installation::{Installation, Observation};
use std::sync::Mutex;

/// Default runtime has no endpoint. Monitoring never establishes authority,
/// enables an operation, or owns the protected service's backend process.
#[derive(Default)]
pub(crate) struct ShellRuntime {
    monitor: Mutex<Option<Monitor>>,
    installation: Mutex<Installation>,
}

impl ShellRuntime {
    #[cfg(test)]
    pub(crate) fn with_installation_fixture(installation: Installation) -> Self {
        Self { monitor: Mutex::new(None), installation: Mutex::new(installation) }
    }

    #[allow(dead_code, reason = "authenticated endpoint bootstrap is not wired")]
    pub fn monitoring(connection: ReaderConnection<Conversation>) -> Self {
        Self {
            monitor: Mutex::new(Some(Monitor::Reader(connection))),
            installation: Mutex::new(Installation::default()),
        }
    }

    #[cfg(windows)]
    #[allow(dead_code, reason = "authenticated native bootstrap is not wired")]
    pub fn native_monitoring(client: NativeClient) -> Self {
        Self {
            monitor: Mutex::new(Some(Monitor::Native(client))),
            installation: Mutex::new(Installation::default()),
        }
    }

    pub fn status(&self) -> Result<ShellStatus, &'static str> {
        let installation = self.installation.lock()
            .map_err(|_| Code::TerminationUnconfirmed.as_str())?.observe();
        if matches!(installation, Observation::Denied | Observation::Unknown) {
            return Err(Code::TerminationUnconfirmed.as_str());
        }
        let mut monitor = self
            .monitor
            .lock()
            .map_err(|_| Code::TerminationUnconfirmed.as_str())?;
        if let Some(connection) = monitor.as_mut() {
            // Poll one event only; blocking Read never runs under this mutex.
            if let Some(code) = connection.poll_fault() {
                return Err(code.as_str());
            }
        }
        // Even active/terminated peer claims and a joined reader do not admit
        // the currently missing authenticated endpoint and approval authority.
        Ok(ShellStatus::unwired())
    }
}

enum Monitor {
    Reader(ReaderConnection<Conversation>),
    #[cfg(windows)]
    Native(NativeClient),
}

impl Monitor {
    fn poll_fault(&mut self) -> Option<Code> {
        match self {
            Self::Reader(connection) => {
                let _ = connection.poll();
                connection.observation().fault
            }
            #[cfg(windows)]
            Self::Native(client) => client.poll().fault,
        }
    }
}

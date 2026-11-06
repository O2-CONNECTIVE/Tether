// Pure local transport calibration. No EdgeDriver, app, Windows or GUI.
use super::*;
use futures_util::future::poll_fn;
use std::pin::Pin;
use tokio::{
    io::{AsyncRead, AsyncWrite, ReadBuf},
    sync::oneshot,
    time::{Duration, timeout},
};

const BODY: &str = r#"{"capabilities":{"alwaysMatch":{"tauri:options":{"application":"fixture.exe","webviewOptions":{"userDataFolder":"SYNTHETIC_PRIVATE_PROFILE"}}}}}"#;
const LIMIT: Duration = Duration::from_secs(3);

async fn exact(stream: &mut tokio::net::TcpStream, bytes: &mut [u8]) {
    let mut filled = 0;
    while filled < bytes.len() {
        let mut buffer = ReadBuf::new(&mut bytes[filled..]);
        poll_fn(|cx| Pin::new(&mut *stream).poll_read(cx, &mut buffer))
            .await
            .unwrap();
        let count = buffer.filled().len();
        assert!(count > 0, "FIXTURE_UNEXPECTED_EOF");
        filled += count;
    }
}

async fn write(stream: &mut tokio::net::TcpStream, bytes: &[u8]) {
    let mut written = 0;
    while written < bytes.len() {
        let count = poll_fn(|cx| Pin::new(&mut *stream).poll_write(cx, &bytes[written..]))
            .await
            .unwrap();
        assert!(count > 0);
        written += count;
    }
}

async fn to_end(stream: &mut tokio::net::TcpStream) -> Vec<u8> {
    let mut bytes = Vec::new();
    loop {
        let mut temporary = [0; 512];
        let mut buffer = ReadBuf::new(&mut temporary);
        poll_fn(|cx| Pin::new(&mut *stream).poll_read(cx, &mut buffer))
            .await
            .unwrap();
        if buffer.filled().is_empty() {
            return bytes;
        }
        bytes.extend_from_slice(buffer.filled());
        assert!(bytes.len() < 8192);
    }
}

async fn read_request(stream: &mut tokio::net::TcpStream) -> Vec<u8> {
    let mut bytes = Vec::new();
    let (head, length) = loop {
        let mut byte = [0];
        exact(stream, &mut byte).await;
        bytes.push(byte[0]);
        assert!(bytes.len() < 8192);
        if bytes.ends_with(b"\r\n\r\n") {
            let text = std::str::from_utf8(&bytes).unwrap().to_ascii_lowercase();
            let size = text
                .lines()
                .find_map(|line| line.strip_prefix("content-length: "))
                .unwrap()
                .trim()
                .parse::<usize>()
                .unwrap();
            break (bytes.len(), size);
        }
    };
    assert!(length < 4096);
    bytes.resize(head + length, 0);
    exact(stream, &mut bytes[head..]).await;
    assert!(bytes.starts_with(b"POST /session HTTP/1.1\r\n"));
    let value: Value = serde_json::from_slice(&bytes[head..]).unwrap();
    assert_eq!(
        value["capabilities"]["alwaysMatch"]["browserName"],
        "webview2"
    );
    assert_eq!(
        value["capabilities"]["alwaysMatch"]["ms:edgeOptions"]["webviewOptions"]["userDataFolder"],
        "SYNTHETIC_PRIVATE_PROFILE"
    );
    bytes
}

async fn intermediary(
    port: u16,
    recorder: std::sync::Arc<diagnostic::Recorder>,
) -> (u16, tokio::task::JoinHandle<()>) {
    let listener = TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0))
        .await
        .unwrap();
    let address = listener.local_addr().unwrap();
    let task = tokio::spawn(async move {
        let (stream, _) = listener.accept().await.unwrap();
        let client = Client::builder(TokioExecutor::new())
            .http1_preserve_header_case(true)
            .http1_title_case_headers(true)
            .retry_canceled_requests(false)
            .build_http();
        let args = Args {
            port: address.port(),
            native_port: port,
            native_host: "127.0.0.1".into(),
            native_driver: None,
        };
        let connection = diagnostic::Connection::open(Some(recorder));
        let request_connection = connection.clone();
        let outcome = auto::Builder::new(TokioExecutor::new())
            .http1()
            .title_case_headers(true)
            .preserve_header_case(true)
            .serve_connection(
                TokioIo::new(stream),
                service_fn(move |request| {
                    handle(
                        client.clone(),
                        request,
                        args.clone(),
                        request_connection.clone(),
                    )
                }),
            )
            .await;
        connection.end(match outcome {
            Ok(()) => diagnostic::Kind::None,
            Err(error) => diagnostic::classify(error.as_ref()),
        });
    });
    (address.port(), task)
}

async fn start_request(port: u16) -> tokio::net::TcpStream {
    let mut stream = tokio::net::TcpStream::connect((std::net::Ipv4Addr::LOCALHOST, port))
        .await
        .unwrap();
    let request = format!(
        "POST /session HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
        BODY.len(),
        BODY
    );
    write(&mut stream, request.as_bytes()).await;
    stream
}

fn terminal(recorder: &diagnostic::Recorder, stage: &str, assigned: bool) {
    let text = recorder.snapshot();
    assert!(!text.contains("SYNTHETIC_PRIVATE") && !text.contains("fixture.exe"));
    let rows: Vec<Value> = text
        .lines()
        .map(|x| serde_json::from_str(x).unwrap())
        .collect();
    let events: Vec<&Value> = rows.iter().filter(|x| x["type"] == "event").collect();
    assert_eq!(
        events
            .iter()
            .filter(|x| x["stage"] == "session-received")
            .count(),
        1
    );
    assert_eq!(
        events
            .iter()
            .filter(|x| x["stage"] == "forward-start")
            .count(),
        1
    );
    let endings: Vec<_> = events
        .iter()
        .filter(|x| {
            matches!(
                x["stage"].as_str(),
                Some("native-headers" | "native-error" | "request-dropped")
            )
        })
        .collect();
    assert_eq!(endings.len(), 1);
    assert_eq!(endings[0]["stage"], stage);
    assert_eq!(endings[0]["connectionAssigned"], assigned);
    assert_eq!(rows.last().unwrap()["dropped"], 0);
    assert_eq!(rows.last().unwrap()["sealed"], true);
}

#[tokio::test]
async fn consumed_post_then_withheld_headers_is_assigned_before_cancellation() {
    timeout(LIMIT, async {
        let native = TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0))
            .await
            .unwrap();
        let (received_tx, received) = oneshot::channel();
        let (release, released) = oneshot::channel();
        let port = native.local_addr().unwrap().port();
        let native_task = tokio::spawn(async move {
            let (mut stream, _) = native.accept().await.unwrap();
            read_request(&mut stream).await;
            received_tx.send(()).unwrap();
            released.await.unwrap();
            drop(stream);
        });
        let recorder = diagnostic::Recorder::memory();
        let (port, server) = intermediary(port, recorder.clone()).await;
        let stream = start_request(port).await;
        received.await.unwrap(); // Barrier: exactly one mapped POST consumed, NO headers sent.
        drop(stream); // Incoming client cancellation, not a fabricated recorder call.
        server.await.unwrap();
        terminal(&recorder, "request-dropped", true);
        release.send(()).unwrap();
        native_task.await.unwrap();
    })
    .await
    .expect("FIXTURE_DEADLINE");
}

#[tokio::test]
async fn complete_response_preserves_body_and_reports_assigned_headers() {
    timeout(LIMIT, async {
        let native = TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0)).await.unwrap();
        let port = native.local_addr().unwrap().port();
        let native_task = tokio::spawn(async move {
            let (mut stream, _) = native.accept().await.unwrap();
            read_request(&mut stream).await;
            write(&mut stream, b"HTTP/1.1 200 OK\r\nContent-Length: 14\r\nConnection: close\r\n\r\n{\"value\":true}").await;
        });
        let recorder = diagnostic::Recorder::memory();
        let (port, server) = intermediary(port, recorder.clone()).await;
        let mut stream = start_request(port).await;
        let response = to_end(&mut stream).await;
        assert!(response.ends_with(b"{\"value\":true}"));
        server.await.unwrap();
        native_task.await.unwrap();
        terminal(&recorder, "native-headers", true);
    }).await.expect("FIXTURE_DEADLINE");
}

#[tokio::test]
async fn zero_port_request_reports_native_error_without_assignment() {
    timeout(LIMIT, async {
        let port = 0; // Synthetic unconnectable endpoint; no OS error subtype claimed.
        let recorder = diagnostic::Recorder::memory();
        let (port, server) = intermediary(port, recorder.clone()).await;
        let mut stream = start_request(port).await;
        let _ = to_end(&mut stream).await;
        server.await.unwrap();
        terminal(&recorder, "native-error", false);
    })
    .await
    .expect("FIXTURE_DEADLINE");
}

#[tokio::test]
async fn headers_then_unfinished_body_is_not_dropped_before_headers() {
    timeout(LIMIT, async {
        let native = TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0))
            .await
            .unwrap();
        let port = native.local_addr().unwrap().port();
        let (release, released) = oneshot::channel();
        let native_task = tokio::spawn(async move {
            let (mut stream, _) = native.accept().await.unwrap();
            read_request(&mut stream).await;
            write(
                &mut stream,
                b"HTTP/1.1 200 OK\r\nContent-Length: 14\r\n\r\n{",
            )
            .await;
            released.await.unwrap();
        });
        let recorder = diagnostic::Recorder::memory();
        let (port, server) = intermediary(port, recorder.clone()).await;
        let mut stream = start_request(port).await;
        let mut response = Vec::new();
        loop {
            let mut byte = [0];
            exact(&mut stream, &mut byte).await;
            response.push(byte[0]);
            if response.ends_with(b"\r\n\r\n{") {
                break;
            }
            assert!(response.len() < 4096);
        }
        drop(stream); // Headers were observed; a later body failure is a separate boundary.
        server.await.unwrap();
        terminal(&recorder, "native-headers", true);
        release.send(()).unwrap();
        native_task.await.unwrap();
    })
    .await
    .expect("FIXTURE_DEADLINE");
}

#[test]
fn probe_schema_keeps_unattached_metadata_null_and_uses_separate_variant() {
    let recorder = diagnostic::Recorder::memory();
    let connection = diagnostic::Connection::open(Some(recorder.clone()));
    let mut request = connection.session();
    request.forward();
    drop(request); // No capture attached: unknown, not false.
    connection.end(diagnostic::Kind::Incomplete);
    let rows: Vec<Value> = recorder
        .snapshot()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    assert_eq!(rows[0]["variant"], "tauri-driver-2.1.0-connection-probe-1");
    for row in rows.iter().filter(|row| row["type"] == "event") {
        assert!(row.as_object().unwrap().contains_key("connectionAssigned"));
        assert!(row["connectionAssigned"].is_null());
    }
}

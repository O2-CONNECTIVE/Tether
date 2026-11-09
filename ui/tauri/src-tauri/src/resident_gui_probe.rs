//! CI-only read-only classification. No paths, identifiers, headers or bodies escape.
use std::ffi::c_void;
#[link(name = "CoreFoundation", kind = "framework")]
unsafe extern "C" {
    fn CFBundleGetMainBundle() -> *const c_void;
    fn CFBundleGetIdentifier(bundle: *const c_void) -> *const c_void;
}
pub(super) fn identity(event: fn(&'static str)) {
    // Both Get functions return borrowed CF objects; neither is dereferenced,
    // retained, released, modified, or converted into a string. No signing proof.
    let bundle = unsafe { CFBundleGetMainBundle() };
    if bundle.is_null() {
        event("main-bundle-absent");
    } else {
        event("main-bundle-present");
        let identifier = unsafe { CFBundleGetIdentifier(bundle) };
        event(if identifier.is_null() {
            "main-bundle-identifier-absent"
        } else {
            "main-bundle-identifier-present"
        });
    }
    event(match std::env::current_exe() {
        Ok(path)
            if path
                .ancestors()
                .any(|p| p.extension().is_some_and(|x| x == "app")) =>
        {
            "executable-in-app"
        }
        Ok(_) => "executable-not-in-app",
        Err(_) => "executable-layout-unavailable",
    });
}
pub(super) fn is_index(uri: &tauri::http::Uri) -> bool {
    uri.scheme_str() == Some("tauri")
        && uri.authority().is_some_and(|a| a.as_str() == "localhost")
        && uri.query().is_none()
        && matches!(uri.path(), "/" | "/index.html")
}
pub(super) fn response(status: u16, mime: Option<&str>, nonempty: bool) -> &'static str {
    if status != 200 {
        "index-response-error"
    } else if !nonempty {
        "index-response-empty"
    } else if mime
        .and_then(|m| m.split(';').next())
        .is_some_and(|m| m.trim().eq_ignore_ascii_case("text/html"))
    {
        "index-response-html"
    } else {
        "index-response-nonhtml"
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn index_probe_excludes_queries_other_paths_and_origins() {
        for path in ["tauri://localhost/", "tauri://localhost/index.html"] {
            assert!(is_index(&path.parse().unwrap()));
        }
        for path in [
            "tauri://localhost/index.html?x=1",
            "tauri://localhost/other",
            "tauri://localhost:42/",
            "https://localhost/",
            "tauri://remote/",
            "tauri://user@localhost/",
        ] {
            assert!(!is_index(&path.parse().unwrap()));
        }
    }
    #[test]
    fn response_probe_only_returns_fixed_classification() {
        assert_eq!(
            response(200, Some("text/html; charset=utf-8"), true),
            "index-response-html"
        );
        assert_eq!(
            response(404, Some("text/html"), true),
            "index-response-error"
        );
        assert_eq!(
            response(200, Some("text/html"), false),
            "index-response-empty"
        );
        assert_eq!(
            response(200, Some("application/json"), true),
            "index-response-nonhtml"
        );
        assert_eq!(response(200, None, true), "index-response-nonhtml");
    }
}

use block2::RcBlock;
use objc2::rc::{autoreleasepool, Retained};
use objc2::runtime::Bool;
use objc2_foundation::{ns_string, NSBundle, NSError};
use objc2_user_notifications::{
    UNAuthorizationOptions, UNAuthorizationStatus, UNMutableNotificationContent,
    UNNotificationRequest, UNNotificationSetting, UNNotificationSettings, UNUserNotificationCenter,
};
use std::path::Path;
use std::ptr::NonNull;
use std::sync::{Arc, Mutex};

pub(super) fn request_permission(
    can_prompt: impl FnOnce() -> bool + Send + 'static,
    completed: impl FnOnce(bool) + Send + 'static,
) {
    with_settings(completed, |settings, completed| {
        if settings.authorizationStatus() != UNAuthorizationStatus::NotDetermined {
            completed(alerts_authorized(settings));
            return;
        }
        if !can_prompt() {
            completed(false);
            return;
        }

        let reply = RcBlock::new(move |granted: Bool, error: *mut NSError| {
            completed(granted.as_bool() && error.is_null());
        });
        UNUserNotificationCenter::currentNotificationCenter()
            .requestAuthorizationWithOptions_completionHandler(
                UNAuthorizationOptions::Alert,
                &reply,
            );
    });
}

pub(super) fn post_if_authorized(
    is_current: impl FnOnce() -> bool + Send + 'static,
    completed: impl FnOnce(bool) + Send + 'static,
) {
    with_settings(completed, |settings, completed| {
        if !alerts_authorized(settings) {
            completed(false);
            return;
        }

        let content = UNMutableNotificationContent::new();
        content.setTitle(ns_string!("Tether"));
        content.setBody(ns_string!("常駐の待機期限まで残り10分以内です。"));
        let request = UNNotificationRequest::requestWithIdentifier_content_trigger(
            ns_string!("tether-standby-deadline"),
            &content,
            None,
        );
        let center = UNUserNotificationCenter::currentNotificationCenter();
        let accepted = Arc::clone(&completed);
        let reply = RcBlock::new(move |error: *mut NSError| accepted(error.is_null()));

        // The lease can change while macOS reads the settings or the request is prepared.
        if !is_current() {
            completed(false);
            return;
        }
        center.addNotificationRequest_withCompletionHandler(&request, Some(&reply));
    });
}

fn alerts_authorized(settings: &UNNotificationSettings) -> bool {
    settings.authorizationStatus() == UNAuthorizationStatus::Authorized
        && settings.alertSetting() == UNNotificationSetting::Enabled
}

fn with_settings(
    completed: impl FnOnce(bool) + Send + 'static,
    read: impl FnOnce(&UNNotificationSettings, Completion) + Send + 'static,
) {
    autoreleasepool(|_| {
        let Some(center) = notification_center() else {
            completed(false);
            return;
        };
        let completed = once(completed);
        let read = Mutex::new(Some(read));
        let reply = RcBlock::new(move |settings: NonNull<UNNotificationSettings>| {
            autoreleasepool(|_| {
                let read = read.lock().unwrap().take();
                if let Some(read) = read {
                    // UserNotifications supplies a live settings object for this callback.
                    read(unsafe { settings.as_ref() }, Arc::clone(&completed));
                }
            });
        });
        center.getNotificationSettingsWithCompletionHandler(&reply);
    });
}

type Completion = Arc<dyn Fn(bool) + Send + Sync>;

fn once(completed: impl FnOnce(bool) + Send + 'static) -> Completion {
    let completed = Mutex::new(Some(completed));
    Arc::new(move |success| {
        let completed = completed.lock().unwrap().take();
        if let Some(completed) = completed {
            completed(success);
        }
    })
}

fn notification_center() -> Option<Retained<UNUserNotificationCenter>> {
    if !objc2::available!(macos = 10.14) {
        return None;
    }
    let bundle = NSBundle::mainBundle();
    let bundle_id = bundle.bundleIdentifier()?;
    let bundle_path = bundle.bundlePath().to_string();
    let bundle_path = Path::new(&bundle_path);
    let executable_path = bundle.executablePath()?.to_string();
    // An unbundled executable can have an embedded identifier. UserNotifications also needs an app bundle.
    if bundle_id.is_empty()
        || bundle_path.extension()? != "app"
        || Path::new(&executable_path).parent()? != bundle_path.join("Contents/MacOS")
    {
        return None;
    }
    Some(UNUserNotificationCenter::currentNotificationCenter())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::mpsc;

    #[test]
    fn unbundled_executable_skips_permission_and_notification_submission() {
        let (send, receive) = mpsc::channel();
        request_permission(
            || panic!("unbundled binary reached the permission check"),
            move |allowed| send.send(allowed).unwrap(),
        );
        assert_eq!(receive.try_recv(), Ok(false));

        let (send, receive) = mpsc::channel();
        post_if_authorized(
            || panic!("unbundled binary reached the submission check"),
            move |accepted| send.send(accepted).unwrap(),
        );
        assert_eq!(receive.try_recv(), Ok(false));
    }
}

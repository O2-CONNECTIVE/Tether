use objc2::MainThreadMarker;
use objc2_app_kit::{NSAlert, NSAlertSecondButtonReturn, NSAlertStyle, NSButtonCell};
use objc2_foundation::ns_string;
#[cfg(not(resident_gui_fixture))]
use tauri::{
    menu::{Menu, MenuItem, MenuItemKind},
    AppHandle,
};

pub(super) const QUIT: &str = "host-menu-bar-quit";

#[cfg(not(resident_gui_fixture))]
pub(super) fn app_menu(app: &AppHandle) -> tauri::Result<Menu<tauri::Wry>> {
    let menu = Menu::default(app)?;
    let unexpected = || std::io::Error::other("unexpected macOS default application menu");
    let Some(MenuItemKind::Submenu(application)) = menu.items()?.into_iter().next() else {
        return Err(unexpected().into());
    };
    // The pinned Tauri default ends its app submenu with the native Quit role.
    // Keep the existing menu and its other system roles.
    let items = application.items()?;
    let quit_position = 7;
    if application.text()? != app.package_info().name
        || items.len() != quit_position + 1
        || !matches!(items.last(), Some(MenuItemKind::Predefined(_)))
    {
        return Err(unexpected().into());
    }
    let quit = MenuItem::with_id(app, QUIT, "終了…", true, Some("CmdOrCtrl+Q"))?;
    application.remove_at(quit_position)?;
    application.insert(&quit, quit_position)?;
    Ok(menu)
}

pub(super) fn confirm() -> bool {
    let Some(main) = MainThreadMarker::new() else {
        return false;
    };
    let alert = NSAlert::new(main);
    alert.setAlertStyle(NSAlertStyle::Warning);
    alert.setMessageText(ns_string!("Tether を終了しますか？"));
    alert.setInformativeText(ns_string!(
        "待機と実行中の作業を停止し、回収を確認してから終了します。"
    ));
    let cancel = alert.addButtonWithTitle(ns_string!("キャンセル"));
    let quit = alert.addButtonWithTitle(ns_string!("終了"));
    let Some(default_button) = cancel
        .cell()
        .and_then(|cell| cell.downcast::<NSButtonCell>().ok())
    else {
        return false;
    };
    cancel.setKeyEquivalent(ns_string!("\u{1b}"));
    quit.setKeyEquivalent(ns_string!(""));
    alert.window().setDefaultButtonCell(Some(&default_button));
    alert.runModal() == NSAlertSecondButtonReturn
}

pub(super) fn show_cleanup() {
    let Some(main) = MainThreadMarker::new() else {
        return;
    };
    let alert = NSAlert::new(main);
    alert.setAlertStyle(NSAlertStyle::Warning);
    alert.setMessageText(ns_string!("要清掃"));
    alert.setInformativeText(ns_string!(
        "回収を確認できないため、Tether は終了しません。"
    ));
    let _ = alert.addButtonWithTitle(ns_string!("閉じる"));
    alert.runModal();
}

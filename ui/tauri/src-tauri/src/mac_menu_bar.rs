use tauri::{
    menu::{Menu, MenuItem},
    tray::TrayIconBuilder,
    AppHandle, Manager, Window, WindowEvent,
};

const TRAY: &str = "host-menu-bar";
const OPEN_WINDOW: &str = "host-menu-bar-open-window";

pub(super) fn install(app: &AppHandle) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, OPEN_WINDOW, "ウィンドウを開く", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&open])?;
    TrayIconBuilder::with_id(TRAY)
        .title("Tether")
        .menu(&menu)
        .on_menu_event(|app, event| {
            if event.id().as_ref() == OPEN_WINDOW {
                let _ = show_main_window(app);
            }
        })
        .build(app)?;
    Ok(())
}

pub(super) fn show_main_window(app: &AppHandle) -> tauri::Result<()> {
    let window = app
        .get_webview_window("main")
        .ok_or(tauri::Error::WindowNotFound)?;
    window.show()?;
    window.set_focus()
}

pub(super) fn window_event(window: &Window, event: &WindowEvent) {
    if let WindowEvent::CloseRequested { api, .. } = event {
        if window.label() == "main" && window.app_handle().tray_by_id(TRAY).is_some() {
            api.prevent_close();
            let _ = window.hide();
        }
    }
}

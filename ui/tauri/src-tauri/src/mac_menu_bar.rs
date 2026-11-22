use tauri::{
    menu::{Menu, MenuEvent, MenuItem},
    tray::TrayIconBuilder,
    AppHandle, Manager, Window, WindowEvent,
};

const TRAY: &str = "host-menu-bar";
const OPEN_WINDOW: &str = "host-menu-bar-open-window";
pub(super) const STOP_STANDBY: &str = "host-menu-bar-stop-standby";

pub(super) fn install(app: &AppHandle) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, OPEN_WINDOW, "ウィンドウを開く", true, None::<&str>)?;
    let stop = MenuItem::with_id(app, STOP_STANDBY, "待機を止める", true, None::<&str>)?;
    let quit = MenuItem::with_id(
        app,
        crate::mac_quit_dialog::QUIT,
        "終了…",
        true,
        None::<&str>,
    )?;
    let menu = Menu::with_items(app, &[&open, &stop, &quit])?;
    TrayIconBuilder::with_id(TRAY)
        .title("Tether")
        .menu(&menu)
        .build(app)?;
    // Application menu (Command-Q) and the status item arrive here once.
    app.on_menu_event(|app, event| {
        let _ = menu_event(app, event);
    });
    Ok(())
}

pub(super) fn menu_event(app: &AppHandle, event: MenuEvent) -> tauri::Result<()> {
    match event.id().as_ref() {
        OPEN_WINDOW => show_main_window(app),
        STOP_STANDBY => {
            let window = app
                .get_webview_window("main")
                .ok_or(tauri::Error::WindowNotFound)?;
            // A payload-free UI signal for the existing local controller. It
            // carries no status, authorization or request identifier.
            window.eval("window.dispatchEvent(new Event('tether-resident-stop'))")
        }
        crate::mac_quit_dialog::QUIT => {
            crate::mac_quit::request(app);
            Ok(())
        }
        _ => Ok(()),
    }
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

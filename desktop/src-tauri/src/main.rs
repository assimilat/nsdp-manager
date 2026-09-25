// ProSAFE Plus desktop shell.
//
// The switch protocol logic lives in the Go `prosafe` binary, shipped here as a
// Tauri sidecar. On startup we spawn `prosafe-server serve`, read the local URL
// it prints, and hand that to the web UI, which talks to it over JSON/HTTP.
use std::sync::Mutex;

use tauri::{Emitter, Manager, State};
use tauri_plugin_shell::process::CommandEvent;
use tauri_plugin_shell::ShellExt;

#[derive(Default)]
struct Backend {
    base: Mutex<Option<String>>,
}

#[tauri::command]
fn api_base(state: State<Backend>) -> Option<String> {
    state.base.lock().unwrap().clone()
}

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .manage(Backend::default())
        .invoke_handler(tauri::generate_handler![api_base])
        .setup(|app| {
            let handle = app.handle().clone();
            let sidecar = app
                .shell()
                .sidecar("prosafe-server")
                .expect("sidecar 'prosafe-server' missing")
                .args(["serve", "--listen", "127.0.0.1:0"]);
            let (mut rx, _child) = sidecar.spawn().expect("failed to spawn prosafe-server");
            tauri::async_runtime::spawn(async move {
                while let Some(event) = rx.recv().await {
                    if let CommandEvent::Stdout(line) = event {
                        let text = String::from_utf8_lossy(&line);
                        if let Some(url) = text.trim().strip_prefix("PROSAFE_LISTEN=") {
                            let url = url.to_string();
                            if let Some(state) = handle.try_state::<Backend>() {
                                *state.base.lock().unwrap() = Some(url.clone());
                            }
                            let _ = handle.emit("api-ready", url);
                        }
                    }
                }
            });
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}

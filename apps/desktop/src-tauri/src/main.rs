use serde_json::Value;
use std::{
    io::{BufRead, BufReader, Write},
    path::PathBuf,
    process::{Child, ChildStdin, Command, Stdio},
    sync::Mutex,
};
use tauri::{AppHandle, Emitter, State};

struct AppServer {
    child: Child,
    stdin: ChildStdin,
}

struct AppState(Mutex<Option<AppServer>>);

fn app_server_path() -> Result<PathBuf, String> {
    if let Some(path) = std::env::var_os("KEPLER_APP_SERVER") {
        return Ok(PathBuf::from(path));
    }

    // In development `make desktop-dev` builds this Go binary first. In a
    // packaged app Tauri places the external binary beside the executable.
    let bundled = std::env::current_exe()
        .map_err(|error| error.to_string())?
        .parent()
        .map(|path| path.join("kepler-agent-app-server"));
    if let Some(path) = bundled.filter(|path| path.exists()) {
        return Ok(path);
    }
    let development = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join("bin/kepler-agent-app-server");
    if development.exists() {
        return Ok(development);
    }
    Err("Kepler app-server binary is missing. Run `make desktop-dev` from the repository root, or install a bundled Kepler app.".into())
}

#[tauri::command]
fn start_server(app: AppHandle, state: State<'_, AppState>, workspace: String) -> Result<(), String> {
    if !PathBuf::from(&workspace).is_dir() {
        return Err("The selected workspace directory does not exist.".into());
    }
    let mut slot = state.0.lock().map_err(|_| "app-server lock poisoned")?;
    if let Some(mut previous) = slot.take() {
        let _ = previous.child.kill();
    }
    let mut child = Command::new(app_server_path()?)
        .arg("--workspace")
        .arg(workspace)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| format!("start app-server: {error}"))?;
    let stdin = child.stdin.take().ok_or("app-server stdin unavailable")?;
    let stdout = child.stdout.take().ok_or("app-server stdout unavailable")?;
    let stderr = child.stderr.take().ok_or("app-server stderr unavailable")?;
    let events = app.clone();
    std::thread::spawn(move || {
        for line in BufReader::new(stdout).lines().map_while(Result::ok) {
            match serde_json::from_str::<Value>(&line) {
                Ok(message) => { let _ = events.emit("appserver-message", message); }
                Err(_) => { let _ = events.emit("appserver-log", line); }
            }
        }
    });
    std::thread::spawn(move || {
        for line in BufReader::new(stderr).lines().map_while(Result::ok) {
            let _ = app.emit("appserver-log", line);
        }
    });
    *slot = Some(AppServer { child, stdin });
    Ok(())
}

#[tauri::command]
fn send_rpc(state: State<'_, AppState>, request: Value) -> Result<(), String> {
    let encoded = serde_json::to_string(&request).map_err(|error| error.to_string())?;
    let mut slot = state.0.lock().map_err(|_| "app-server lock poisoned")?;
    let server = slot.as_mut().ok_or("Kepler is not connected to a workspace")?;
    writeln!(server.stdin, "{encoded}").map_err(|error| format!("write app-server request: {error}"))?;
    server.stdin.flush().map_err(|error| format!("flush app-server request: {error}"))
}

fn main() {
    tauri::Builder::default()
        .manage(AppState(Mutex::new(None)))
        .invoke_handler(tauri::generate_handler![start_server, send_rpc])
        .run(tauri::generate_context!())
        .expect("error while running Kepler desktop");
}

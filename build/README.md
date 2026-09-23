# Build P0

The task files target the official Wails `v3.0.0-beta.23` source pinned by the
application module. Bootstrap the matching CLI with
`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.23`
and put the Go binary directory on PATH. After that,
`wails3 task common:wails:install` can reinstall the pinned CLI.
The Go entrypoint is `./cmd/app`, the product name is
`WazzapAgent`, and the application identifier is
`io.github.chomosuke9.wazzapagent`.

`wails3 task build`, `wails3 task package`, `wails3 task run`, and
`wails3 task dev` select the current
desktop host. The desktop tasks generate bindings, build `frontend/dist`, and
compile the GUI with the project `gui` tag. Windows is configured for a
no-cgo executable; Linux and macOS require their native Wails WebView toolchain
on the host that runs the task.

For P0, `package` produces a production executable only: it does not yet create
an installer, macOS `.app` bundle, signed package, or auto-update distribution.
Linux and macOS tasks are scaffolding and have not been run on those hosts.

The Android task is an explicit gate. Beta.23 requires a generated
`build/android` Gradle project, SDK API 35, build-tools, NDK
`26.3.11579264`, JDK, npm, and cgo. `wails3 task android:toolchain:check`
runs `wails3 doctor` without installing anything. `wails3 task android:build`
refuses to claim an APK until that native project has been generated and
verified on a supported macOS or Linux host. It deliberately has no adb
install/uninstall
step, because uninstalling would erase application persistence.

The common tasks keep binding generation separate from the frontend build:
`common:generate:bindings` or `common:generate:bindings:android` is followed
by `common:build:frontend`. `NPM` can override the npm command, for example
`wails3 task NPM=path/to/npm build`; no machine-specific path is committed.
